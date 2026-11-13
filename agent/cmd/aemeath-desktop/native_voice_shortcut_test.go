package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

type shortcutRegistry struct {
	callbacks map[string]func(bool)
	stops     map[string]int
	fail      string
	calls     []string
}

func newShortcutRegistry() *shortcutRegistry {
	return &shortcutRegistry{callbacks: map[string]func(bool){}, stops: map[string]int{}}
}
func (r *shortcutRegistry) register(key string, cb func(bool)) (func(), error) {
	r.calls = append(r.calls, key)
	if key == r.fail {
		return nil, errors.New("occupied")
	}
	r.callbacks[key] = cb
	return func() { r.stops[key]++ }, nil
}

func TestConfiguredShortcutStartupFallbackAndExplicitConflict(t *testing.T) {
	for _, custom := range []bool{false, true} {
		a, _, _ := voiceHoldTest(t)
		r := newShortcutRegistry()
		r.fail = "F8"
		if custom {
			a.snapshot.Settings.VoiceShortcut = "F8"
		}
		stop := a.installConfiguredVoiceHold(r.register)
		if custom {
			if a.voiceGlobalReady || len(r.calls) != 1 || !strings.Contains(a.voiceGlobalError, "occupied") {
				t.Fatal("explicit conflict silently fell back")
			}
		} else if !a.voiceGlobalReady || a.voiceGlobalKey != "Ctrl+Shift+F8" {
			t.Fatal("automatic fallback failed")
		}
		stop()
		stop()
	}
}

func TestCustomShortcutSaveSwitchCancelsHoldAndRejectsStaleEvents(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	vf := &nativeVoiceFake{nativeFake: f}
	a.service = vf
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.Session = 9
	a.draft.Voice.Enabled = true
	a.refresh()
	r := newShortcutRegistry()
	stop := a.installConfiguredVoiceHold(r.register)
	defer stop()
	r.callbacks["F8"](true)
	(<-updates)()
	if !a.voiceHold {
		t.Fatal("initial key failed")
	}
	a.draft.VoiceShortcut = " shift + ctrl + space "
	a.edited()
	a.save()
	if a.voiceGlobalKey != "F8" || r.stops["F8"] != 0 {
		t.Fatal("old key stopped before persistence")
	}
	nativeComplete(t, updates, tt)
	if a.voiceGlobalKey != "Ctrl+Shift+Space" || a.dirty || a.draft.VoiceShortcut != "Ctrl+Shift+Space" || r.stops["F8"] != 1 || a.voiceHold {
		t.Fatal("switch did not commit")
	}
	if len(f.saves) != 1 || f.saves[0].VoiceShortcut != "Ctrl+Shift+Space" {
		t.Fatal("shortcut not persisted")
	}
	r.callbacks["F8"](true)
	(<-updates)()
	r.callbacks["F8"](false)
	(<-updates)()
	r.callbacks["Ctrl+Shift+Space"](true)
	(<-updates)()
	r.callbacks["Ctrl+Shift+Space"](false)
	(<-updates)()
	want := [][2]string{{"record_start", ""}, {"record_cancel", ""}, {"record_start", ""}, {"record_stop", ""}}
	if !reflect.DeepEqual(vf.commands, want) {
		t.Fatalf("commands=%v", vf.commands)
	}
}

func TestCustomShortcutSaveFailuresPreserveOldBinding(t *testing.T) {
	for _, failure := range []string{"format", "conflict", "persist"} {
		t.Run(failure, func(t *testing.T) {
			a, f, updates, tt := nativeTest(t)
			r := newShortcutRegistry()
			stop := a.installConfiguredVoiceHold(r.register)
			defer stop()
			a.draft.VoiceShortcut = "F9"
			a.edited()
			switch failure {
			case "format":
				a.draft.VoiceShortcut = "A"
			case "conflict":
				r.fail = "F9"
			case "persist":
				f.saveError = errors.New("disk full")
			}
			a.save()
			if failure == "persist" {
				nativeComplete(t, updates, tt)
			}
			if a.voiceGlobalKey != "F8" || !a.voiceGlobalReady || r.stops["F8"] != 0 || !a.dirty || len(f.saves) != 0 {
				t.Fatal("failed save changed binding or draft")
			}
			if failure == "persist" && r.stops["F9"] != 1 {
				t.Fatal("failed save leaked reservation")
			}
		})
	}
}

func TestCustomShortcutSameKeyDefaultAndConcurrentDraft(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	r := newShortcutRegistry()
	stop := a.installConfiguredVoiceHold(r.register)
	defer stop()
	a.draft.VoiceShortcut = "F8"
	a.edited()
	a.save()
	nativeComplete(t, updates, tt)
	if len(r.calls) != 1 {
		t.Fatal("same key re-registered")
	}
	a.draft.VoiceShortcut = "F9"
	a.edited()
	a.save()
	a.draft.VoiceShortcut = "F10"
	a.edited()
	a.save()
	nativeComplete(t, updates, tt)
	if a.voiceGlobalKey != "F9" || a.draft.VoiceShortcut != "F10" || !a.dirty || len(f.saves) != 2 {
		t.Fatal("concurrent edit lost or duplicate save")
	}
	a.draft.VoiceShortcut = ""
	a.edited()
	a.save()
	nativeComplete(t, updates, tt)
	if a.voiceGlobalKey != "F8" || a.draft.VoiceShortcut != "" {
		t.Fatal("restore default failed")
	}
}

func TestCustomShortcutShutdownDuringPendingSave(t *testing.T) {
	a, _, updates, tt := nativeTest(t)
	r := newShortcutRegistry()
	stop := a.installConfiguredVoiceHold(r.register)
	a.draft.VoiceShortcut = "F9"
	a.edited()
	a.save()
	r.callbacks["F8"](true)
	stop()
	stop()
	// Both persistence completion and native event are dispatched, in either order.
	for range 2 {
		nativeComplete(t, updates, tt)
	}
	if a.voiceGlobalReady || r.stops["F8"] != 1 || r.stops["F9"] != 1 {
		t.Fatal("shutdown leaked/revived listener")
	}
}

func TestCustomShortcutSettingsInputAndDefault(t *testing.T) {
	a, _, _, _ := nativeTest(t)
	tt := ui.NewTester(a.voiceShortcutSettingsView, 680, 700)
	tt.Frame()
	if _, ok := tt.Find("按住说话快捷键"); !ok {
		t.Fatal("shortcut input missing")
	}
	a.draft.VoiceShortcut = "A"
	tt.Frame()
	if _, ok := tt.Find("快捷键格式错误"); !ok {
		t.Fatal("invalid shortcut feedback missing")
	}
	nativeClick(t, tt, "恢复默认快捷键")
	if a.draft.VoiceShortcut != "" || !a.dirty {
		t.Fatal("default action failed")
	}
}
