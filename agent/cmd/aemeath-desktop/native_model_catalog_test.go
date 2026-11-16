package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"aemeath/agent/internal/brain"

	"github.com/egoist/mygo/ui"
)

var nativeCatalogFixture = []brain.ModelCatalogProvider{
	{ID: "anthropic", Models: []string{"shared-model", "claude-test"}},
	{ID: "openai", Models: []string{"shared-model", "exact-model"}},
}

func nativeCatalogTest(t *testing.T) (*nativeUI, *nativeFake, chan func(), *ui.Tester) {
	t.Helper()
	a, f, updates, tt := nativeTest(t)
	a.draft.Voice.ContextWindowMode = "auto"
	a.draft.Voice.ModelsDevEnabled = true
	a.draft.Voice.LLMModel = "request-model"
	a.modelCatalogPicker.load = func(context.Context) ([]brain.ModelCatalogProvider, error) { return nativeCatalogFixture, nil }
	tt.SetSize(1100, 1000)
	nativeClick(t, tt, "设置")
	nativeVoiceNavigate(t, tt, "models.dev 模型")
	return a, f, updates, tt
}

func TestNativeCatalogPickerSearchSelectAndSave(t *testing.T) {
	a, f, updates, tt := nativeCatalogTest(t)
	before := a.draft.Voice
	if a.dirty || a.modelCatalogPicker.open {
		t.Fatal("viewing picker changed draft or opened dialog")
	}
	for _, label := range []string{"models.dev 服务商 ID", "models.dev 模型 ID"} {
		if _, ok := tt.AccessByRole(ui.RoleTextField, label); ok {
			t.Fatal("catalog mapping should not offer manual ID inputs")
		}
	}
	nativeVoiceClickRole(t, tt, ui.RoleButton, "models.dev 模型")
	if _, ok := tt.AccessByRole(ui.RoleDialog, "选择 models.dev 模型"); !ok {
		t.Fatal("catalog picker dialog did not open")
	}
	nativeComplete(t, updates, tt)
	if a.draft.Voice != before || a.dirty || len(f.saves) != 0 {
		t.Fatal("catalog download edited or saved draft")
	}
	if !tt.Focused("搜索服务商或模型") {
		t.Fatal("search should receive dialog focus")
	}
	tt.Type("OPENAI shared")
	if _, ok := tt.AccessByRole(ui.RoleButton, "anthropic / shared-model"); ok {
		t.Fatal("search mixed models from other providers")
	}
	nativeClick(t, tt, "openai / shared-model")
	if a.modelCatalogPicker.open || !a.dirty || a.revision != 1 || a.draft.Voice.ModelsDevProvider != "openai" || a.draft.Voice.ModelsDevModel != "shared-model" || a.draft.Voice.LLMModel != "request-model" {
		t.Fatalf("selection did not atomically set catalog mapping: %+v", a.draft.Voice)
	}
	if !tt.HasText("openai / shared-model") {
		t.Fatal("combined form did not show selected mapping")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Voice.ModelsDevProvider != "openai" || f.saves[0].Voice.ModelsDevModel != "shared-model" {
		t.Fatal("combined mapping was not saved")
	}
}

func TestNativeCatalogPickerCancelAutomaticAndSameChoice(t *testing.T) {
	for _, action := range []string{"取消", "escape", "自动匹配", "openai / exact-model"} {
		t.Run(action, func(t *testing.T) {
			a, _, updates, tt := nativeCatalogTest(t)
			a.draft.Voice.ModelsDevProvider = "openai"
			a.draft.Voice.ModelsDevModel = "exact-model"
			tt.Frame()
			nativeVoiceClickRole(t, tt, ui.RoleButton, "models.dev 模型")
			nativeComplete(t, updates, tt)
			if action == "escape" {
				tt.Key(0, ui.KeyEscape)
			} else {
				button, ok := tt.AccessByRole(ui.RoleButton, action)
				if !ok {
					t.Fatalf("missing catalog action: %s", action)
				}
				r := button.Bounds
				tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
			}
			if a.modelCatalogPicker.open {
				t.Fatal("dialog did not close")
			}
			if action == "自动匹配" {
				if a.draft.Voice.ModelsDevProvider != "" || a.draft.Voice.ModelsDevModel != "" || !a.dirty {
					t.Fatal("automatic choice did not clear both overrides")
				}
			} else if a.draft.Voice.ModelsDevProvider != "openai" || a.draft.Voice.ModelsDevModel != "exact-model" || a.dirty {
				t.Fatal("cancel or identical choice changed mapping")
			}
		})
	}
}

func TestNativeCatalogPickerErrorRetryAndEmptySearch(t *testing.T) {
	a, _, updates, tt := nativeCatalogTest(t)
	a.modelCatalogPicker.load = func(context.Context) ([]brain.ModelCatalogProvider, error) {
		return nil, errors.New("private upstream error")
	}
	nativeVoiceClickRole(t, tt, ui.RoleButton, "models.dev 模型")
	nativeComplete(t, updates, tt)
	if !tt.HasText("获取 models.dev 目录失败") || tt.HasText("private upstream error") || a.dirty {
		t.Fatal("catalog failure was not safely displayed")
	}
	a.modelCatalogPicker.load = func(context.Context) ([]brain.ModelCatalogProvider, error) { return nativeCatalogFixture, nil }
	nativeClick(t, tt, "重试")
	nativeComplete(t, updates, tt)
	nativeClick(t, tt, "搜索服务商或模型")
	tt.Type("not-a-model")
	if !tt.HasText("无匹配模型") || a.dirty {
		t.Fatal("empty search edited draft or failed to show empty state")
	}
	nativeClick(t, tt, "取消")
}

func TestNativeCatalogPickerIgnoresSupersededLoad(t *testing.T) {
	a, _, updates, tt := nativeCatalogTest(t)
	a.openModelCatalog()
	a.openModelCatalog()
	nativeComplete(t, updates, tt)
	nativeComplete(t, updates, tt)
	if a.modelCatalogPicker.loading || !reflect.DeepEqual(a.modelCatalogPicker.providers, nativeCatalogFixture) || a.dirty {
		t.Fatal("superseded load corrupted current picker")
	}
}

func TestNativeCatalogPickerLargeCatalogCompactLayout(t *testing.T) {
	for _, size := range [][2]int{{760, 577}, {1100, 900}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			a, _, updates, tt := nativeCatalogTest(t)
			tt.SetSize(size[0], size[1])
			models := make([]string, 2500)
			for i := range models {
				models[i] = fmt.Sprintf("model-%04d", i)
			}
			a.modelCatalogPicker.load = func(context.Context) ([]brain.ModelCatalogProvider, error) {
				return []brain.ModelCatalogProvider{{ID: "provider", Models: models}}, nil
			}
			nativeVoiceClickRole(t, tt, ui.RoleButton, "models.dev 模型")
			nativeComplete(t, updates, tt)
			dialog, ok := tt.AccessByRole(ui.RoleDialog, "选择 models.dev 模型")
			if !ok || dialog.Bounds.X < 0 || dialog.Bounds.Y < 0 || dialog.Bounds.X+dialog.Bounds.W > float32(size[0]) || dialog.Bounds.Y+dialog.Bounds.H > float32(size[1]) {
				t.Fatalf("catalog dialog exceeds window bounds: %+v", dialog.Bounds)
			}
			tt.Type("model-2499")
			nativeClick(t, tt, "provider / model-2499")
			if a.draft.Voice.ModelsDevProvider != "provider" || a.draft.Voice.ModelsDevModel != "model-2499" || a.modelCatalogPicker.open {
				t.Fatal("large catalog search did not select requested model")
			}
		})
	}
}

func TestNativeCatalogChoicesSearchByProviderAndModel(t *testing.T) {
	for _, tc := range []struct {
		query string
		count int
	}{
		{"", 4}, {" OPENAI shared ", 1}, {"SHARED", 2}, {"claude-test", 1}, {"absent", 0},
	} {
		if got := nativeCatalogChoices(nativeCatalogFixture, tc.query); len(got) != tc.count {
			t.Fatalf("search %q: got %v", tc.query, got)
		}
	}
}
