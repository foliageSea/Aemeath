package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeAPIKeysCanBeRevealedIndependentlyAndHiddenAfterEditing(t *testing.T) {
	a, _, _, tt := nativeTest(t)
	tt.SetSize(760, 2200)
	a.draft.Voice.TTSProvider = "fish"
	a.draft.Voice.LLMAPIKey = "fixture-openai-key"
	a.draft.Voice.MiMoAPIKey = "fixture-mimo-key"
	a.draft.Voice.FishAPIKey = "fixture-fish-key"
	before := a.draft.Voice
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	fields := []struct{ label, value string }{{"OpenAI API 密钥", before.LLMAPIKey}, {"MiMo API 密钥", before.MiMoAPIKey}, {"Fish API 密钥", before.FishAPIKey}}
	for _, field := range fields {
		info, ok := tt.AccessByRole(ui.RoleTextField, field.label)
		if !ok || !info.Password || strings.Contains(info.Value, field.value) {
			t.Fatalf("key not hidden by default: %s", field.label)
		}
	}
	for _, field := range fields {
		nativeClick(t, tt, "显示 "+field.label)
		info, ok := tt.AccessByRole(ui.RoleTextField, field.label)
		if !ok || info.Password || info.Value != field.value {
			t.Fatalf("key reveal failed: %s", field.label)
		}
		for _, other := range fields {
			if other.label == field.label {
				continue
			}
			info, _ := tt.AccessByRole(ui.RoleTextField, other.label)
			if !info.Password {
				t.Fatalf("revealing one key revealed another: %s", other.label)
			}
		}
		a.refresh()
		tt.Frame()
		info, _ = tt.AccessByRole(ui.RoleTextField, field.label)
		if info.Password || info.Value != field.value {
			t.Fatal("refresh changed reveal state or draft")
		}
		nativeClick(t, tt, "隐藏 "+field.label)
		info, _ = tt.AccessByRole(ui.RoleTextField, field.label)
		if !info.Password || strings.Contains(info.Value, field.value) {
			t.Fatal("hide did not restore password masking")
		}
	}
	if a.dirty || a.draft.Voice != before {
		t.Fatal("visibility toggle edited the saved configuration")
	}
	nativeClick(t, tt, "显示 OpenAI API 密钥")
	nativeVoiceEdit(t, tt, "OpenAI API 密钥", "edited-key")
	nativeClick(t, tt, "隐藏 OpenAI API 密钥")
	if a.draft.Voice.LLMAPIKey != "edited-key" || !a.dirty {
		t.Fatal("hiding discarded the edited key")
	}
	for _, label := range []string{"显示 OpenAI API 密钥", "OpenAI API 密钥"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 {
			t.Fatalf("key control exceeds minimum width: %s %+v", label, r)
		}
	}
}
