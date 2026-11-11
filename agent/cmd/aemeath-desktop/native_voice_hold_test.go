package main

import (
	"errors"
	"reflect"
	"strings"
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

func TestGlobalVoiceHoldBackgroundPressRepeatReleaseAndQuickTap(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	a.page = "概览"
	tt.SetFocused(false)
	a.globalVoiceKey(true)
	a.globalVoiceKey(true)
	if !a.voiceHold {
		t.Fatal("background press did not start")
	}
	tt.Frame()
	if !a.voiceHold {
		t.Fatal("blur or another page canceled global recording")
	}
	a.globalVoiceKey(false)
	a.globalVoiceKey(false)
	a.globalVoiceKey(true)
	a.globalVoiceKey(false)
	want := [][2]string{{"record_start", ""}, {"record_stop", ""}, {"record_start", ""}, {"record_stop", ""}}
	if a.voiceHold || !reflect.DeepEqual(f.commands, want) {
		t.Fatalf("commands=%v held=%v", f.commands, a.voiceHold)
	}
}

func TestGlobalVoiceHoldEscapeCancelsWithoutRepeatRestart(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	a.globalVoiceKey(true)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	a.globalVoiceKey(true)
	a.globalVoiceKey(false)
	want := [][2]string{{"record_start", ""}, {"record_cancel", ""}}
	if a.voiceHold || !reflect.DeepEqual(f.commands, want) {
		t.Fatalf("canceled hold restarted: %v", f.commands)
	}
	a.globalVoiceKey(true)
	a.globalVoiceKey(false)
	if len(f.commands) != 4 {
		t.Fatal("fresh press failed")
	}
}

func TestGlobalVoiceHoldUsesFreshSnapshotOnBothEdges(t *testing.T) {
	a, f, _ := voiceHoldTest(t)
	f.state.Voice.Connected = false
	a.globalVoiceKey(true)
	a.globalVoiceKey(false)
	if len(f.commands) != 0 {
		t.Fatal("stale online snapshot started recording")
	}
	f.state.Voice.Connected = true
	a.globalVoiceKey(true)
	f.state.Voice.Session++
	a.globalVoiceKey(false)
	if a.voiceHold || len(f.commands) != 1 {
		t.Fatalf("release reached replacement session: %v", f.commands)
	}
}

func TestGlobalVoiceHoldUnavailableAndStartFailure(t *testing.T) {
	for _, mode := range []string{"disabled", "disconnected", "error"} {
		t.Run(mode, func(t *testing.T) {
			a, f, _ := voiceHoldTest(t)
			switch mode {
			case "disabled":
				f.state.Settings.Voice.Enabled = false
			case "disconnected":
				f.state.Voice.Connected = false
			case "error":
				f.commandError = errors.New("queue full")
			}
			a.globalVoiceKey(true)
			a.globalVoiceKey(true)
			a.globalVoiceKey(false)
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

func TestGlobalVoiceHoldBackendCompletionRequiresFreshPress(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	a.globalVoiceKey(true)
	f.state.Voice.Turn++
	f.state.Voice.Phase = "recognizing"
	a.refresh()
	tt.Frame()
	a.globalVoiceKey(true)
	a.globalVoiceKey(false)
	if a.voiceHold || len(f.commands) != 1 {
		t.Fatalf("completed hold restarted: %v", f.commands)
	}
}

func TestGlobalVoiceHoldRegistrationDispatchAndShutdown(t *testing.T) {
	a, f, _ := voiceHoldTest(t)
	pending := make(chan func(), 8)
	a.update = func(fn func()) { pending <- fn }
	var emit func(bool)
	stops := 0
	stop := a.installGlobalVoiceHold(func(cb func(bool)) (func(), error) {
		emit = cb
		return func() { stops++ }, nil
	}, nil)
	emit(true)
	if len(f.commands) != 0 {
		t.Fatal("native callback mutated UI outside dispatch")
	}
	(<-pending)()
	// Queue a release before stopping: queued callbacks must become inert.
	emit(false)
	stop()
	stop()
	(<-pending)()
	emit(true)
	(<-pending)()
	want := [][2]string{{"record_start", ""}, {"record_cancel", ""}}
	if stops != 1 || a.voiceGlobalReady || a.voiceHold || !reflect.DeepEqual(f.commands, want) {
		t.Fatalf("cleanup stops=%d commands=%v", stops, f.commands)
	}
}

func TestGlobalVoiceHoldRegistrationConflictIsVisible(t *testing.T) {
	a, _, tt := voiceHoldTest(t)
	stop := a.installGlobalVoiceHold(func(func(bool)) (func(), error) { return nil, errors.New("F8 already registered") }, nil)
	stop()
	tt.Frame()
	if a.voiceGlobalReady || !strings.Contains(a.voiceGlobalError, "already registered") || !tt.HasText(a.voiceGlobalError) {
		t.Fatal("shortcut conflict not shown")
	}
}

func TestGlobalVoiceHoldFallsBackAndDisplaysBinding(t *testing.T) {
	a, f, tt := voiceHoldTest(t)
	a.update = func(fn func()) { fn() }
	var emit func(bool)
	stop := a.installGlobalVoiceHold(
		func(func(bool)) (func(), error) { return nil, errors.New("F8 occupied") },
		func(cb func(bool)) (func(), error) { emit = cb; return func() {}, nil },
	)
	defer stop()
	tt.Frame()
	if !a.voiceGlobalReady || a.voiceGlobalKey != "Ctrl+Shift+F8" || !tt.HasText("Ctrl+Shift+F8 按住说话") {
		t.Fatal("fallback binding not displayed")
	}
	nativeVoiceAssertTooltip(t, tt, "语音快捷键提示", "全局 Ctrl+Shift+F8 · 后台也可按住说话 · 松开发送 · 最长 30 秒")
	emit(true)
	emit(false)
	if len(f.commands) != 2 || f.commands[1][0] != "record_stop" {
		t.Fatal("fallback did not record and submit")
	}
}

func TestLocalF8DoesNotDuplicateGlobalListener(t *testing.T) {
	_, f, tt := voiceHoldTest(t)
	tt.Key(0, ui.KeyF8)
	if len(f.commands) != 0 {
		t.Fatal("local F8 still owns recording")
	}
}
