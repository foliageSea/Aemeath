package main

import (
	"errors"
	"testing"

	"aemeath/agent/internal/desktop"

	"github.com/egoist/mygo/ui"
)

func nativeAssertPlaybackSelection(t *testing.T, tt *ui.Tester, selected string) {
	t.Helper()
	for _, label := range []string{"Agent", "监控台"} {
		info, ok := tt.AccessByRole(ui.RoleRadio, label)
		if !ok || info.Checked != (label == selected) {
			t.Fatalf("playback option %q: %+v, want selected %v", label, info, label == selected)
		}
	}
}

func TestNativeVoicePlaybackQuickSwitch(t *testing.T) {
	for _, initial := range []string{"", "agent", "console"} {
		t.Run("initial="+initial, func(t *testing.T) {
			a, f, updates, tt := nativeTest(t)
			f.state.Settings.Voice.PlaybackOutput = initial
			a.draft = copySettings(f.state.Settings)
			a.refresh()
			nativeClick(t, tt, "语音助手")
			selected := []string{"Agent", "监控台"}[nativeVoicePlaybackIndex(initial)]
			nativeAssertPlaybackSelection(t, tt, selected)
			if len(f.saves) != 0 || a.dirty {
				t.Fatal("opening voice page changed settings")
			}
			choices := []struct{ label, route, path string }{
				{"Agent", "console", "语音路径：ESP32 麦克风 → 本机扬声器"},
				{"监控台", "agent", "语音路径：ESP32 麦克风 → ESP32 扬声器"},
			}
			if initial == "console" {
				choices[0], choices[1] = choices[1], choices[0]
			}
			for i, choice := range choices {
				nativeClick(t, tt, choice.label)
				nativeComplete(t, updates, tt)
				nativeAssertPlaybackSelection(t, tt, choice.label)
				if len(f.saves) != i+1 || f.saves[i].Voice.PlaybackOutput != choice.route || a.draft.Voice.PlaybackOutput != choice.route || a.dirty || !tt.HasText(choice.path) {
					t.Fatalf("quick switch did not persist %s and update the path", choice.label)
				}
			}
			nativeClick(t, tt, "设置")
			nativeClick(t, tt, "语音")
			nativeClick(t, tt, "声音试听")
			nativeAssertPlaybackSelection(t, tt, choices[1].label)
		})
	}
}

func TestNativeVoicePlaybackQuickSwitchPreservesDraft(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	a.draft.Address = "unsaved.local"
	a.draft.Voice.Persona = "unsaved persona"
	a.edited()
	revision := a.revision
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "Agent")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Address != "saved.local" || f.saves[0].Voice.Persona != "" || a.draft.Address != "unsaved.local" || a.draft.Voice.Persona != "unsaved persona" || !a.dirty || a.revision != revision {
		t.Fatal("quick switch saved or discarded unrelated draft edits")
	}
}

func TestNativeVoicePlaybackQuickSwitchFailure(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	f.saveError = errors.New("disk full")
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "Agent")
	nativeComplete(t, updates, tt)
	nativeAssertPlaybackSelection(t, tt, "监控台")
	if len(f.saves) != 0 || a.draft.Voice.PlaybackOutput != "" || a.noticeType != nativeNoticeError || a.busy["save"] {
		t.Fatal("failed quick switch changed output or failed to report the error")
	}
	f.saveError = nil
	nativeClick(t, tt, "Agent")
	nativeComplete(t, updates, tt)
	nativeAssertPlaybackSelection(t, tt, "Agent")
}

func TestNativeVoicePlaybackQuickSwitchPending(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	gate := make(chan struct{})
	f.gate = gate
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "Agent")
	for _, label := range []string{"Agent", "监控台"} {
		info, ok := tt.AccessByRole(ui.RoleRadio, label)
		if !ok || !info.Disabled {
			t.Fatalf("pending option is enabled: %s", label)
		}
	}
	a.switchVoicePlaybackOutput("agent")
	a.save()
	// A new settings edit must survive completion of the in-flight quick switch.
	a.draft.Voice.PlaybackOutput = "agent"
	a.edited()
	close(gate)
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || a.draft.Voice.PlaybackOutput != "agent" || !a.dirty {
		t.Fatal("pending save lost draft or started duplicate saves")
	}
	nativeAssertPlaybackSelection(t, tt, "Agent")
}

func TestNativeVoicePlaybackQuickSwitchStandalone(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Settings.RunMode = desktop.RunModeStandalone
	f.state.Settings.Voice.PlaybackOutput = "agent"
	a.draft = copySettings(f.state.Settings)
	a.refresh()
	nativeClick(t, tt, "语音助手")
	nativeAssertPlaybackSelection(t, tt, "Agent")
	for _, label := range []string{"Agent", "监控台"} {
		info, ok := tt.AccessByRole(ui.RoleRadio, label)
		if !ok || !info.Disabled {
			t.Fatalf("standalone option enabled: %s", label)
		}
	}
	a.switchVoicePlaybackOutput("console")
	if a.busy["save"] || len(f.saves) != 0 || a.draft.Voice.PlaybackOutput != "agent" {
		t.Fatal("standalone changed device playback preference")
	}
}

func TestNativeVoicePlaybackQuickSwitchLayout(t *testing.T) {
	for _, size := range [][2]int{{760, 577}, {863, 577}, {1100, 800}} {
		for _, dark := range []bool{false, true} {
			_, _, _, tt := nativeTest(t)
			tt.SetSize(size[0], size[1])
			tt.SetDark(dark)
			nativeClick(t, tt, "语音助手")
			row, ok := tt.Find("播放位置快捷切换")
			if !ok || row.W <= 0 || row.X < 206 || row.X+row.W > float32(size[0]) {
				t.Fatalf("shortcut outside window: %+v", row)
			}
			for _, label := range []string{"Agent", "监控台"} {
				r, ok := tt.Find(label)
				if !ok || r.W <= 0 || r.X < row.X || r.X+r.W > row.X+row.W || r.Y < row.Y || r.Y+r.H > row.Y+row.H {
					t.Fatalf("option outside shortcut: %s %+v row=%+v", label, r, row)
				}
			}
		}
	}
}

func TestNativeVoicePlaybackOutputSettings(t *testing.T) {
	for _, initial := range []string{"", "agent", "console"} {
		t.Run("initial="+initial, func(t *testing.T) {
			a, f, updates, tt := nativeTest(t)
			a.draft.Voice.PlaybackOutput = initial
			tt.SetSize(1100, 800)
			nativeClick(t, tt, "设置")
			nativeClick(t, tt, "语音")
			nativeClick(t, tt, "声音试听")
			selected := "监控台"
			if initial == "console" {
				selected = "Agent"
			}
			nativeAssertPlaybackSelection(t, tt, selected)
			if a.draft.Voice.PlaybackOutput != initial || a.dirty {
				t.Fatal("opening settings changed the existing playback route")
			}
			if !tt.HasText("Agent 使用当前电脑的扬声器；监控台使用 ESP32 设备扬声器。保存后生效。") {
				t.Fatal("playback description does not match the physical destinations")
			}
			choices := []struct{ label, route string }{{"Agent", "console"}, {"监控台", "agent"}}
			if initial == "console" {
				choices[0], choices[1] = choices[1], choices[0]
			}
			for i, choice := range choices {
				nativeClick(t, tt, choice.label)
				if a.draft.Voice.PlaybackOutput != choice.route || !a.dirty {
					t.Fatalf("%s not bound to route %q: %+v", choice.label, choice.route, a.draft.Voice)
				}
				nativeAssertPlaybackSelection(t, tt, choice.label)
				nativeClick(t, tt, "保存设置")
				nativeComplete(t, updates, tt)
				if len(f.saves) != i+1 || f.saves[i].Voice.PlaybackOutput != choice.route || a.dirty {
					t.Fatalf("save lost %s playback route", choice.label)
				}
				reopened := nativeState(f)
				if reopened.draft.Voice.PlaybackOutput != choice.route {
					t.Fatalf("reopened settings lost %s output", choice.label)
				}
				reopened.voiceSettingsSection = 3
				reopenedTester := ui.NewTester(reopened.voiceSettingsView, 900, 800)
				nativeAssertPlaybackSelection(t, reopenedTester, choice.label)
			}
		})
	}
}
