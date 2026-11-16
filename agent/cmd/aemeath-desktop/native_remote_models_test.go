package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"aemeath/agent/internal/netproxy"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeRemoteModelsFetchSelectAndSave(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 1600)
	nativeClick(t, tt, "设置")
	nativeVoiceEdit(t, tt, "OpenAI 服务地址", "https://draft.example/v1/")
	nativeVoiceEdit(t, tt, "OpenAI API 密钥", "draft-secret")
	nativeVoiceEdit(t, tt, "对话模型", "custom-model")
	type request struct{ baseURL, apiKey string }
	requests := make(chan request, 2)
	gate := make(chan struct{})
	a.remoteModels.fetch = func(ctx context.Context, baseURL, apiKey string) ([]string, error) {
		requests <- request{baseURL, apiKey}
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("missing deadline")
		}
		<-gate
		return []string{"alpha", "beta"}, nil
	}
	revision := a.revision
	nativeVoiceClickRole(t, tt, ui.RoleButton, "获取模型")
	a.fetchRemoteModels()
	if button, ok := tt.AccessByRole(ui.RoleButton, "获取模型"); !ok || !button.Disabled {
		t.Fatal("fetch button must be disabled during request")
	}
	close(gate)
	nativeComplete(t, updates, tt)
	if got := <-requests; got != (request{"https://draft.example/v1", "draft-secret"}) {
		t.Fatalf("request did not use draft: %+v", got)
	}
	if len(requests) != 0 {
		t.Fatal("duplicate request")
	}
	if a.revision != revision || a.draft.Voice.LLMModel != "custom-model" || len(f.saves) != 0 {
		t.Fatal("fetch changed or saved settings")
	}
	if !reflect.DeepEqual(a.remoteModels.models, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected models: %v", a.remoteModels.models)
	}
	if _, ok := tt.AccessByRole(ui.RoleDialog, "选择对话模型"); !ok || !tt.Focused("搜索模型") {
		t.Fatal("fetched models did not open a searchable dialog")
	}
	tt.Type("BET")
	if _, ok := tt.AccessByRole(ui.RoleButton, "alpha"); ok {
		t.Fatal("search did not filter models")
	}
	nativeClick(t, tt, "beta")
	if a.remoteModels.open {
		t.Fatal("selection did not close dialog")
	}
	if field, ok := tt.AccessByRole(ui.RoleTextField, "对话模型"); !ok || field.Value != "beta" {
		t.Fatalf("selection did not fill model form: %+v", field)
	}
	if a.draft.Voice.LLMModel != "beta" || a.revision <= revision || !a.dirty {
		t.Fatalf("selection did not edit draft: %q", a.draft.Voice.LLMModel)
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Voice.LLMModel != "beta" {
		t.Fatalf("selected model not saved: %+v", f.saves)
	}
	nativeVoiceEdit(t, tt, "对话模型", "manual-model")
	if a.draft.Voice.LLMModel != "manual-model" {
		t.Fatal("manual model editing stopped working")
	}
	nativeVoiceEdit(t, tt, "OpenAI API 密钥", "changed-secret")
	if _, ok := tt.AccessByRole(ui.RoleComboBox, "远程模型"); ok {
		t.Fatal("models from old credentials still visible")
	}
}

func TestNativeRemoteModelsDiscardStaleResults(t *testing.T) {
	for _, change := range []string{"url", "key"} {
		t.Run(change, func(t *testing.T) {
			a, _, updates, tt := nativeTest(t)
			a.draft.Voice.LLMBaseURL = "https://old.example/v1"
			a.draft.Voice.LLMAPIKey = "old-key"
			a.remoteModels.fetch = func(context.Context, string, string) ([]string, error) {
				return []string{"stale-model"}, nil
			}
			a.fetchRemoteModels()
			if change == "url" {
				a.draft.Voice.LLMBaseURL = "https://new.example/v1"
			} else {
				a.draft.Voice.LLMAPIKey = "new-key"
			}
			nativeComplete(t, updates, tt)
			if a.busy["remote-models"] || len(a.remoteModels.models) != 0 || a.notice != "" {
				t.Fatal("stale result was applied")
			}
		})
	}
}

func TestNativeRemoteModelsFailuresRetryAndPreserveDraft(t *testing.T) {
	a, _, updates, tt := nativeTest(t)
	a.draft.Voice.LLMModel = "keep-model"
	for _, tc := range []struct {
		models []string
		err    error
		notice string
		kind   nativeNoticeType
	}{
		{err: errors.New("HTTP 401"), notice: "获取模型失败", kind: nativeNoticeError},
		{notice: "未获取到可用模型", kind: nativeNoticeWarning},
		{models: []string{"new-model"}},
	} {
		a.remoteModels.fetch = func(context.Context, string, string) ([]string, error) { return tc.models, tc.err }
		a.fetchRemoteModels()
		nativeComplete(t, updates, tt)
		if a.busy["remote-models"] || (len(tc.models) == 0 && (!strings.Contains(a.notice, tc.notice) || a.noticeType != tc.kind)) || a.remoteModels.open != (len(tc.models) > 0) {
			t.Fatalf("unexpected request status: %q %s", a.notice, a.noticeType)
		}
		if a.draft.Voice.LLMModel != "keep-model" || a.dirty {
			t.Fatal("request changed draft model")
		}
	}
}

func TestNativeRemoteModelsUsesSavedNetwork(t *testing.T) {
	var savedCalls, draftCalls atomic.Int32
	saved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		savedCalls.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer draft-key" {
			t.Error("incorrect model request through saved proxy")
		}
		io.WriteString(w, `{"data":[{"id":"proxy-model"}]}`)
	}))
	defer saved.Close()
	draft := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { draftCalls.Add(1) }))
	defer draft.Close()
	router, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: saved.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	a, f, updates, tt := nativeTest(t)
	a.service = &nativeAssetsNetworkService{nativeFake: f, router: router}
	a.draft.Network = netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: draft.URL}
	a.draft.Voice.LLMBaseURL = "http://models.example.test/v1"
	a.draft.Voice.LLMAPIKey = "draft-key"
	a.fetchRemoteModels()
	nativeComplete(t, updates, tt)
	if savedCalls.Load() != 1 || draftCalls.Load() != 0 || !reflect.DeepEqual(a.remoteModels.models, []string{"proxy-model"}) {
		t.Fatalf("model request ignored saved proxy: %s", a.notice)
	}
	if len(f.saves) != 0 || a.draft.Network.ProxyURL != draft.URL {
		t.Fatal("model fetch changed network draft")
	}
}

func TestNativeRemoteModelsDialogCancelAndSameSelection(t *testing.T) {
	for _, action := range []string{"取消", "escape", "keep-model", "stale"} {
		t.Run(action, func(t *testing.T) {
			a, _, _, tt := nativeTest(t)
			a.draft.Voice.LLMModel = "keep-model"
			nativeClick(t, tt, "设置")
			nativeVoiceNavigate(t, tt, "对话模型")
			a.remoteModels.source = a.modelSource()
			a.remoteModels.models = []string{"keep-model", "other-model"}
			a.remoteModels.open = true
			tt.Frame()
			switch action {
			case "escape":
				tt.Key(0, ui.KeyEscape)
			case "stale":
				a.draft.Voice.LLMAPIKey = "changed"
				tt.Frame()
			default:
				nativeClick(t, tt, action)
			}
			if a.remoteModels.open || a.draft.Voice.LLMModel != "keep-model" || a.dirty {
				t.Fatal("dismissing dialog or reselecting current model changed draft")
			}
			if _, ok := tt.AccessByRole(ui.RoleDialog, "选择对话模型"); ok {
				t.Fatal("dialog remained visible")
			}
			if _, ok := tt.AccessByRole(ui.RoleComboBox, "远程模型"); ok {
				t.Fatal("inline remote model form should not exist")
			}
		})
	}
}

func TestNativeRemoteModelsWithoutWindow(t *testing.T) {
	a, _, _, _ := nativeTest(t)
	a.update = nil
	a.fetchRemoteModels()
	if a.busy["remote-models"] || a.noticeType != nativeNoticeWarning {
		t.Fatal("fetch without dispatcher did not remain retryable")
	}
}
