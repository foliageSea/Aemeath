package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeAPIKeysCanBeRevealedIndependentlyAndHiddenAfterEditing(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(760, 2200)
	a.draft.Voice.TTSProvider = "fish"
	a.draft.Voice.LLMAPIKey = "fixture-openai-key"
	a.draft.Voice.MiMoAPIKey = "fixture-mimo-key"
	a.draft.Voice.FishAPIKey = "fixture-fish-key"
	a.draft.Voice.TavilyAPIKey = "fixture-tavily-key"
	before := a.draft.Voice
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	fields := []struct{ label, value string }{{"OpenAI API 密钥", before.LLMAPIKey}, {"MiMo API 密钥", before.MiMoAPIKey}, {"Fish API 密钥", before.FishAPIKey}, {"Tavily API 密钥", before.TavilyAPIKey}}
	for _, field := range fields {
		nativeVoiceNavigate(t, tt, field.label)
		info, ok := tt.AccessByRole(ui.RoleTextField, field.label)
		if !ok || !info.Password || strings.Contains(info.Value, field.value) {
			t.Fatalf("key not hidden by default: %s", field.label)
		}
	}
	for _, field := range fields {
		nativeVoiceNavigate(t, tt, field.label)
		nativeClick(t, tt, "显示 "+field.label)
		info, ok := tt.AccessByRole(ui.RoleTextField, field.label)
		if !ok || info.Password || info.Value != field.value {
			t.Fatalf("key reveal failed: %s", field.label)
		}
		for _, other := range fields {
			if other.label == field.label {
				continue
			}
			nativeVoiceNavigate(t, tt, other.label)
			info, _ := tt.AccessByRole(ui.RoleTextField, other.label)
			if !info.Password {
				t.Fatalf("revealing one key revealed another: %s", other.label)
			}
		}
		nativeVoiceNavigate(t, tt, field.label)
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
	nativeVoiceNavigate(t, tt, "OpenAI API 密钥")
	nativeClick(t, tt, "显示 OpenAI API 密钥")
	nativeVoiceEdit(t, tt, "OpenAI API 密钥", "edited-key")
	nativeClick(t, tt, "隐藏 OpenAI API 密钥")
	if a.draft.Voice.LLMAPIKey != "edited-key" || !a.dirty {
		t.Fatal("hiding discarded the edited key")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Voice.TavilyAPIKey != before.TavilyAPIKey {
		t.Fatal("Tavily key not included in saved settings")
	}
	if reopened := nativeState(f); reopened.draft.Voice.TavilyAPIKey != before.TavilyAPIKey {
		t.Fatal("reopened settings lost Tavily key")
	}
	for _, label := range []string{"显示 OpenAI API 密钥", "OpenAI API 密钥", "显示 Tavily API 密钥", "Tavily API 密钥"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 {
			t.Fatalf("key control exceeds minimum width: %s %+v", label, r)
		}
	}
}

func TestNativeTavilyKeyEditableInCompactSettings(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeVoiceNavigate(t, tt, "OpenAI API 密钥")
	tt.Scroll(650, 430, 0, 3000)
	for _, label := range []string{"Tavily API 密钥", "显示 Tavily API 密钥"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 370 || r.X+r.W > 740 || r.Y < 126 || r.Y+r.H > 580 {
			t.Fatalf("Tavily key control cannot scroll into compact view: %s %+v", label, r)
		}
	}
	nativeVoiceEdit(t, tt, "Tavily API 密钥", "compact-tavily-key")
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if a.dirty || len(f.saves) != 1 || f.saves[0].Voice.TavilyAPIKey != "compact-tavily-key" {
		t.Fatal("compact Tavily input did not save")
	}
}
