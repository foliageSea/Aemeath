package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeFishReferenceIDMaskedKeyAndDraftPersistence(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 3200)
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "Fish")
	if a.draft.Voice.TTSProvider != "fish" || !a.dirty {
		t.Fatal("Fish selection not bound to draft")
	}
	fields := []struct{ label, value string }{
		{"Fish 服务地址", "https://fish.example"},
		{"Fish API 密钥", "fixture-fish-private-key"},
		{"Fish 模型", "s2.1-pro"},
		{"Fish reference_id", "my-saved-voice"},
		{"Fish 参考音频转写", "参考音频的逐字转写。"},
		{"参考音频路径", "/fixtures/reference.wav"},
	}
	for _, field := range fields {
		nativeVoiceEdit(t, tt, field.label, field.value)
	}
	nativeClick(t, tt, "低延迟")
	cfg := a.draft.Voice
	if cfg.FishBaseURL != fields[0].value || cfg.FishAPIKey != fields[1].value || cfg.FishModel != fields[2].value || cfg.FishReferenceID != fields[3].value || cfg.FishReferenceText != fields[4].value || cfg.ReferenceAudio != fields[5].value || cfg.FishLatency != "low" {
		t.Fatal("Fish form fields not bound")
	}
	info, ok := tt.AccessByRole(ui.RoleTextField, "Fish API 密钥")
	if !ok || !info.Password || strings.Contains(info.Value, fields[1].value) {
		t.Fatal("Fish key exposed through accessibility")
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, fields[1].value) {
			t.Fatal("Fish key rendered as plain text")
		}
	}
	nativeClick(t, tt, "MiMo")
	if a.draft.Voice.TTSProvider != "mimo" || a.draft.Voice.FishReferenceID != "my-saved-voice" {
		t.Fatal("switching provider lost Fish draft")
	}
	nativeClick(t, tt, "Fish")
	a.refresh()
	tt.Frame()
	if a.draft.Voice != cfg {
		t.Fatal("snapshot or provider roundtrip overwrote Fish draft")
	}
	gate := make(chan struct{})
	f.gate = gate
	nativeClick(t, tt, "保存设置")
	nativeVoiceEdit(t, tt, "Fish reference_id", "newer-voice")
	close(gate)
	nativeComplete(t, updates, tt)
	if !a.dirty || a.draft.Voice.FishReferenceID != "newer-voice" {
		t.Fatal("pending save discarded newer Fish reference_id")
	}
	f.mu.Lock()
	first := f.saves[0].Voice
	f.mu.Unlock()
	if first != cfg {
		t.Fatal("save did not capture complete Fish configuration")
	}
	nativeClick(t, tt, "保存语音设置")
	nativeComplete(t, updates, tt)
	reopened := nativeState(f)
	if reopened.draft.Voice != a.draft.Voice || reopened.draft.Voice.FishAPIKey != fields[1].value {
		t.Fatal("reopened settings lost Fish provider/reference/key")
	}
}
