package main

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeCapturedShortcut(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   ui.InputEvent
		want string
	}{
		{"function", ui.InputEvent{Key: ui.KeyF8}, "F8"},
		{"combined", ui.InputEvent{Key: ui.KeySpace, Mods: ui.Ctrl | ui.Shift}, "Ctrl+Shift+Space"},
		{"all modifiers", ui.InputEvent{Key: ui.KeyK, Mods: ui.Super | ui.Alt | ui.Shift | ui.Ctrl}, "Ctrl+Shift+Alt+Super+K"},
		{"physical layout", ui.InputEvent{Key: ui.KeyQ, PhysicalKey: ui.KeyA, Mods: ui.Super}, "Super+A"},
		{"code layout", ui.InputEvent{Key: ui.KeyQ, Code: ui.CodeKeyA, Mods: ui.Ctrl}, "Ctrl+A"},
		{"shifted digit", ui.InputEvent{Code: ui.CodeDigit1, Mods: ui.Alt | ui.Shift}, "Shift+Alt+1"},
		{"zero", ui.InputEvent{Code: ui.CodeDigit0, Mods: ui.Ctrl}, "Ctrl+0"},
		{"extended F13", ui.InputEvent{Code: ui.CodeF13}, "F13"},
		{"extended F24", ui.InputEvent{Code: ui.CodeF24}, "F24"},
		{"plain letter", ui.InputEvent{Key: ui.KeyA}, ""},
		{"shift alone", ui.InputEvent{Key: ui.KeySpace, Mods: ui.Shift}, ""},
		{"unsupported", ui.InputEvent{Key: ui.KeyEnter}, ""},
		{"unsupported physical", ui.InputEvent{Key: ui.Key1, Code: ui.CodeNumpad1, Mods: ui.Ctrl}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nativeCapturedShortcutForPlatform(tc.ev, "darwin")
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestShortcutCaptureWindowsLayoutMatchesVirtualKeyRegistration(t *testing.T) {
	for _, ev := range []ui.InputEvent{
		{Key: ui.KeyA, Code: ui.CodeKeyQ, Mods: ui.Ctrl}, // AZERTY
		{Key: ui.KeyA, PhysicalKey: ui.KeyQ, Mods: ui.Ctrl},
	} {
		value, err := nativeCapturedShortcutForPlatform(ev, "windows")
		if err != nil || value != "Ctrl+A" {
			t.Fatalf("Windows virtual-key capture = %q, %v", value, err)
		}
	}
}

func TestShortcutCaptureConfirmAndSave(t *testing.T) {
	a, f, updates, _ := nativeTest(t)
	r := newShortcutRegistry()
	defer a.installConfiguredVoiceHold(r.register)()
	tt := ui.NewTester(a.voiceShortcutSettingsView, 680, 700)
	tt.Frame()
	nativeClick(t, tt, "录入快捷键")
	if !a.voiceShortcutCapture.open || !tt.Focused("快捷键录入区域") {
		t.Fatal("capture did not open with keyboard focus")
	}
	tt.Key(ui.Ctrl|ui.Shift, ui.KeySpace)
	if a.voiceShortcutCapture.value != "Ctrl+Shift+Space" || a.dirty || a.draft.VoiceShortcut != "" {
		t.Fatal("capture must preview without changing draft")
	}
	tt.KeyRepeat(0, ui.KeyF9)
	if a.voiceShortcutCapture.value != "Ctrl+Shift+Space" {
		t.Fatal("auto-repeat replaced candidate")
	}
	nativeClick(t, tt, "使用快捷键")
	if a.voiceShortcutCapture.open || !a.dirty || a.draft.VoiceShortcut != "Ctrl+Shift+Space" || a.voiceGlobalKey != "F8" || len(f.saves) != 0 {
		t.Fatal("confirmation must only change draft")
	}
	a.save()
	nativeComplete(t, updates, tt)
	if a.dirty || a.voiceGlobalKey != "Ctrl+Shift+Space" || len(f.saves) != 1 {
		t.Fatal("captured shortcut failed to save/register")
	}
}

func TestShortcutCaptureInvalidCancelAndReopen(t *testing.T) {
	a, _, _, _ := nativeTest(t)
	a.draft.VoiceShortcut = "F9"
	tt := ui.NewTester(a.voiceShortcutSettingsView, 680, 700)
	tt.Frame()
	nativeClick(t, tt, "录入快捷键")
	tt.CodeDown(ui.Ctrl, ui.CodeControlLeft)
	if a.voiceShortcutCapture.value != "" || a.voiceShortcutCapture.error != "" {
		t.Fatal("modifier alone must wait for main key")
	}
	tt.Key(0, ui.KeyA)
	if a.voiceShortcutCapture.error == "" {
		t.Fatal("invalid key feedback missing")
	}
	if confirm, ok := tt.AccessByRole(ui.RoleButton, "使用快捷键"); !ok || !confirm.Disabled {
		t.Fatal("invalid capture can be confirmed")
	}
	tt.Key(0, ui.KeyF8)
	if a.voiceShortcutCapture.value != "F8" || a.voiceShortcutCapture.error != "" {
		t.Fatal("valid retry did not clear error")
	}
	tt.Key(0, ui.KeyEscape)
	if a.voiceShortcutCapture.open || a.draft.VoiceShortcut != "F9" || a.dirty {
		t.Fatal("escape changed draft")
	}
	nativeClick(t, tt, "录入快捷键")
	if a.voiceShortcutCapture.value != "" || a.voiceShortcutCapture.error != "" {
		t.Fatal("reopen retained previous preview")
	}
	tt.Key(0, ui.KeyF10)
	nativeClick(t, tt, "取消录入")
	if a.voiceShortcutCapture.open || a.draft.VoiceShortcut != "F9" || a.dirty {
		t.Fatal("cancel changed draft")
	}
}

func TestShortcutCaptureTabNavigation(t *testing.T) {
	a, _, _, _ := nativeTest(t)
	tt := ui.NewTester(a.voiceShortcutSettingsView, 680, 700)
	tt.Frame()
	nativeClick(t, tt, "录入快捷键")
	tt.Key(0, ui.KeyF9)
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("使用快捷键") {
		t.Fatal("cannot tab to confirm")
	}
	tt.Key(0, ui.KeyEnter)
	if a.voiceShortcutCapture.open || a.draft.VoiceShortcut != "F9" {
		t.Fatal("keyboard confirmation failed")
	}
}

func TestShortcutCaptureSuppressesGlobalHoldAndCancelsExisting(t *testing.T) {
	a, f, _ := voiceHoldTest(t)
	a.voiceGlobalKey = "F8"
	a.globalVoiceKey(true)
	a.beginVoiceShortcutCapture()
	a.globalVoiceKey(false)
	a.globalVoiceKey(true)
	if a.voiceHold || a.voiceShortcutCapture.value != "F8" {
		t.Fatal("global binding was not captured safely")
	}
	want := [][2]string{{"record_start", ""}, {"record_cancel", ""}}
	if !reflect.DeepEqual(f.commands, want) {
		t.Fatalf("unexpected commands: %v", f.commands)
	}
	a.voiceShortcutCapture.open = false
	a.globalVoiceKey(true) // Still-held key must not restart after cancellation.
	a.globalVoiceKey(false)
	if !reflect.DeepEqual(f.commands, want) {
		t.Fatal("captured hold leaked into recording")
	}
	a.globalVoiceKey(true)
	a.globalVoiceKey(false)
	if len(f.commands) != 4 {
		t.Fatal("fresh press after capture did not resume")
	}
}
