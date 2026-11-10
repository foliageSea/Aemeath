package main

import (
	"testing"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

type nativeMemoryFake struct {
	*nativeVoiceFake
	turns []brain.MemoryTurn
}

func (f *nativeMemoryFake) VoiceConversation() []brain.MemoryTurn { return f.turns }

func TestNativeRestoresVoiceTextWithoutInventedDiagnostics(t *testing.T) {
	_, base, _, _ := nativeTest(t)
	f := &nativeMemoryFake{nativeVoiceFake: &nativeVoiceFake{nativeFake: base}, turns: []brain.MemoryTurn{
		{User: "我叫小明", Assistant: "你好，小明。"},
	}}
	a := nativeState(f)
	a.loginRead = func() bool { return false }
	a.page = "语音助手"
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	for _, text := range []string{"我叫小明", "你好，小明。", "历史对话 · 仅恢复问答文字"} {
		if !tt.HasText(text) {
			t.Fatalf("restored text not shown: %s", text)
		}
	}
	if tt.HasText("执行过程") || tt.HasText("语音识别") {
		t.Fatal("restored text invented execution diagnostics")
	}
	for i := 0; i < 3; i++ {
		a.refresh()
	}
	if len(a.voiceHistory) != 1 || !a.voiceHistory[0].restored {
		t.Fatal("refresh duplicated restored conversation")
	}

	// Reconnection starts Turn at 1 again; neither restored history nor the
	// last live exchange may be overwritten by that recycled number.
	base.state.Voice = voice.State{Session: 1, Turn: 1, Transcript: "第一个问题", Response: "第一个回答"}
	a.refresh()
	base.state.Voice.Session = 2
	base.state.Voice.Transcript = "重连后的问题"
	a.refresh()
	if len(a.voiceHistory) != 3 || a.voiceHistory[1].transcript != "第一个问题" || a.voiceHistory[2].transcript != "重连后的问题" {
		t.Fatalf("reconnect overwrote history: %+v", a.voiceHistory)
	}
}

func TestNativeRestoreDeduplicatesCurrentSnapshot(t *testing.T) {
	_, base, _, _ := nativeTest(t)
	base.state.Voice = voice.State{Session: 7, Turn: 1, Transcript: "你好", Response: "你好呀"}
	f := &nativeMemoryFake{nativeVoiceFake: &nativeVoiceFake{nativeFake: base}, turns: []brain.MemoryTurn{
		{User: "你好", Assistant: "你好呀"},
	}}
	a := nativeState(f)
	if len(a.voiceHistory) != 1 || a.voiceHistory[0].restored || a.voiceHistory[0].state.Session != 7 {
		t.Fatalf("current exchange was duplicated or lost its runtime state: %+v", a.voiceHistory)
	}
}

func TestNativeClearDoesNotSuppressFirstTurnAfterReconnect(t *testing.T) {
	a, base, updates, tt := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	base.state.Settings.Voice.Enabled = true
	base.state.Voice = voice.State{Connected: true, Session: 1, Turn: 1, Transcript: "旧问题", Response: "旧回答"}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "清空对话")
	nativeComplete(t, updates, tt)
	a.refresh()
	if len(a.voiceHistory) != 0 {
		t.Fatal("cleared snapshot reappeared")
	}
	base.state.Voice.Session = 2
	base.state.Voice.Transcript = "新问题"
	a.refresh()
	if len(a.voiceHistory) != 1 || a.voiceHistory[0].transcript != "新问题" {
		t.Fatal("reused turn number after reconnect was incorrectly suppressed")
	}
}
