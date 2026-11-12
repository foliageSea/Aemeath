package main

import (
	"testing"

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
