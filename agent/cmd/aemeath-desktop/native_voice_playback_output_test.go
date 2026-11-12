package main

import "testing"

func TestNativeVoicePlaybackOutputSettings(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 800)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "声音试听")
	if a.draft.Voice.PlaybackOutput != "" && a.draft.Voice.PlaybackOutput != "agent" {
		t.Fatalf("existing settings should play on Agent: %q", a.draft.Voice.PlaybackOutput)
	}
	nativeClick(t, tt, "监控台")
	if a.draft.Voice.PlaybackOutput != "console" || !a.dirty {
		t.Fatalf("console output not bound to draft: %+v", a.draft.Voice)
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Voice.PlaybackOutput != "console" {
		t.Fatal("saved settings lost console output")
	}
	reopened := nativeState(f)
	if reopened.draft.Voice.PlaybackOutput != "console" {
		t.Fatal("reopened settings lost console output")
	}
	nativeClick(t, tt, "Agent")
	if a.draft.Voice.PlaybackOutput != "agent" || !a.dirty {
		t.Fatal("switching back to Agent did not update draft")
	}
}
