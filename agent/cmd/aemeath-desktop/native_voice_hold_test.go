package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func voiceHoldTest(t *testing.T) (*nativeUI, *nativeVoiceFake, *ui.Tester) {
	t.Helper()
	a, base, _, tt := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.Session = 7
	a.refresh()
	nativeClick(t, tt, "语音助手")
	return a, f, tt
}

func TestVoiceHoldF8PressRepeatReleaseAndQuickTap(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	tt.KeyDown(ui.KeyF8)
	if !a.voiceHold || !tt.HasText("正在按住录音 · 松开 F8 发送 · Esc 取消") {
		t.Fatal("hold did not start")
	}
	tt.KeyRepeat(0, ui.KeyF8)
	tt.KeyRepeat(0, ui.KeyF8)
	tt.KeyUp(ui.KeyF8)
	if a.voiceHold {
		t.Fatal("release did not end hold")
	}
	tt.Key(0, ui.KeyF8)
	want := [][2]string{{"record_start", ""}, {"record_stop", ""}, {"record_start", ""}, {"record_stop", ""}}
	if !reflect.DeepEqual(f.commands, want) {
		t.Fatalf("commands %v, want %v", f.commands, want)
	}
}

func TestVoiceHoldEscBlurAndNavigationDiscard(t *testing.T) {
	for _, action := range []string{"escape", "blur", "navigate"} {
		t.Run(action, func(t *testing.T) {
			a, f, tt := voiceHoldTest(t)
			tt.KeyDown(ui.KeyF8)
			switch action {
			case "escape":
				tt.Key(0, ui.KeyEscape)
			case "blur":
				tt.SetFocused(false)
			case "navigate":
				nativeClick(t, tt, "概览")
			}
			tt.Frame()
			tt.KeyUp(ui.KeyF8)
			want := [][2]string{{"record_start", ""}, {"record_cancel", ""}}
			if a.voiceHold || !reflect.DeepEqual(f.commands, want) {
				t.Fatalf("held=%v commands=%v", a.voiceHold, f.commands)
			}
		})
	}
}

func TestVoiceHoldUnavailableAndStartFailure(t *testing.T) {
	for _, mode := range []string{"disabled", "disconnected", "wrong-page", "error"} {
		t.Run(mode, func(t *testing.T) {
			a, f, tt := voiceHoldTest(t)
			switch mode {
			case "disabled":
				f.state.Settings.Voice.Enabled = false
			case "disconnected":
				f.state.Voice.Connected = false
			case "wrong-page":
				nativeClick(t, tt, "概览")
			case "error":
				f.commandError = errors.New("queue full")
			}
			a.refresh()
			tt.Frame()
			tt.Key(0, ui.KeyF8)
			if a.voiceHold {
				t.Fatal("unavailable recording started")
			}
			want := 0
			if mode == "error" {
				want = 1
			}
			if len(f.commands) != want {
				t.Fatalf("unexpected commands: %v", f.commands)
			}
		})
	}
}

func TestVoiceHoldEscapeIgnoresRepeatsUntilFreshPress(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	tt.KeyDown(ui.KeyF8)
	tt.Key(0, ui.KeyEscape)
	tt.KeyRepeat(0, ui.KeyF8)
	tt.KeyUp(ui.KeyF8)
	if a.voiceHold || len(f.commands) != 2 {
		t.Fatalf("canceled hold restarted: %v", f.commands)
	}
	tt.Key(0, ui.KeyF8)
	if len(f.commands) != 4 || f.commands[2][0] != "record_start" || f.commands[3][0] != "record_stop" {
		t.Fatalf("fresh press did not recover: %v", f.commands)
	}
}

func TestVoiceHoldWorksWithoutFocusedControl(t *testing.T) {
	a, f, _ := voiceHoldTest(t)
	tt := ui.NewTester(a.view, 760, 600)
	tt.Key(0, ui.KeyF8)
	if a.voiceHold || len(f.commands) != 2 || f.commands[1][0] != "record_stop" {
		t.Fatalf("unfocused control lost key release: %v", f.commands)
	}
}

func TestVoiceHoldBackendCompletionClearsRecordingHint(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	tt.KeyDown(ui.KeyF8)
	f.state.Voice.Turn++
	f.state.Voice.Phase = "recognizing"
	a.refresh()
	tt.Frame()
	tt.KeyRepeat(0, ui.KeyF8)
	tt.KeyUp(ui.KeyF8)
	if a.voiceHold || len(f.commands) != 1 {
		t.Fatalf("completed hold restarted or submitted twice: %v", f.commands)
	}
}

func TestVoiceHoldReleaseDoesNotAffectReplacementSession(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	tt.KeyDown(ui.KeyF8)
	f.state.Voice.Session++
	a.refresh()
	tt.Frame()
	tt.KeyUp(ui.KeyF8)
	if a.voiceHold || len(f.commands) != 1 {
		t.Fatalf("stale release reached new session: %v", f.commands)
	}
}
