package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeSettingsTabsKeepDraftAndSaveAcrossTabs(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "设置")
	nativeVoiceEdit(t, tt, "设备地址", "draft.local")
	for _, label := range []string{"通用", "屏幕", "语音"} {
		nativeClick(t, tt, label)
		info, ok := tt.AccessByRole(ui.RoleTab, label)
		if !ok || !info.Selected {
			t.Fatalf("tab not selected: %s %+v", label, info)
		}
		if _, ok := tt.AccessByRole(ui.RoleTextField, "设备地址"); ok {
			t.Fatal("inactive tab contents still displayed")
		}
	}
	nativeVoiceEdit(t, tt, "对话模型", "draft-model")
	nativeClick(t, tt, "连接")
	a.refresh()
	tt.Frame()
	if a.draft.Address != "draft.local" || a.draft.Voice.LLMModel != "draft-model" || !a.dirty {
		t.Fatal("tab switch lost draft")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Address != "draft.local" || f.saves[0].Voice.LLMModel != "draft-model" {
		t.Fatal("save did not include other tab edits")
	}
	nativeClick(t, tt, "语音助手")
	if _, ok := tt.AccessByRole(ui.RoleTextField, "对话模型"); ok {
		t.Fatal("voice configuration remains in conversation")
	}
	nativeClick(t, tt, "语音设置")
	if a.page != "设置" || a.settingsTab != 3 {
		t.Fatal("voice settings shortcut did not select tab")
	}
}

func TestNativeConversationRetainsTurnsAndClearHandlesStaleSnapshot(t *testing.T) {
	a, base, updates, tt := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.Turn = 1
	f.state.Voice.Transcript = "第一问"
	a.refresh()
	f.state.Voice.Response = "第一答"
	a.refresh()
	a.refresh()
	if len(a.voiceHistory) != 1 || a.voiceHistory[0].response != "第一答" {
		t.Fatal("turn update duplicated messages")
	}
	f.state.Voice.Turn = 2
	f.state.Voice.Transcript, f.state.Voice.Response = "第二问", "第二答"
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{"第一问", "第一答", "第二问", "第二答"} {
		if !tt.HasText(text) {
			t.Fatalf("missing conversation: %s", text)
		}
	}
	f.commandError = errors.New("clear failed")
	nativeClick(t, tt, "清空对话")
	nativeComplete(t, updates, tt)
	if len(a.voiceHistory) != 2 {
		t.Fatal("failed clear lost history")
	}
	f.commandError = nil
	nativeClick(t, tt, "清空对话")
	nativeComplete(t, updates, tt)
	a.refresh() // Command acceptance may precede the device's next snapshot.
	if len(a.voiceHistory) != 0 {
		t.Fatal("stale snapshot restored cleared messages")
	}
	f.state.Voice.Turn = 3
	f.state.Voice.Transcript, f.state.Voice.Response = "新问题", "新回复"
	a.refresh()
	if len(a.voiceHistory) != 1 || a.voiceHistory[0].transcript != "新问题" {
		t.Fatal("new conversation did not resume")
	}
}

func TestNativeVoiceEmptyLayoutWithConnectionWarning(t *testing.T) {
	for _, size := range [][2]int{{760, 577}, {863, 577}, {1100, 800}} {
		for _, dark := range []bool{false, true} {
			a, f, _, tt := nativeTest(t)
			a.service = &nativeVoiceFake{nativeFake: f}
			tt.SetSize(size[0], size[1])
			tt.SetDark(dark)
			f.state.Error = "device unavailable"
			f.state.Settings.Voice.Enabled = true
			a.refresh()
			nativeClick(t, tt, "语音助手")
			body, ok := tt.Find("对话记录")
			if !ok || body.H < 80 {
				t.Fatalf("no usable conversation space: %+v", body)
			}
			title, ok := tt.Find("和爱弥斯聊一聊")
			if !ok || abs(int(title.X+title.W/2-body.X-body.W/2)) > 2 || title.Y < body.Y || title.Y+title.H > body.Y+body.H {
				t.Fatalf("empty state is not centered in conversation: title=%+v body=%+v", title, body)
			}
			for _, label := range []string{"语音操作栏", "手动唤醒", "取消对话", "清空对话", "语音设置"} {
				r, ok := tt.Find(label)
				if !ok || r.W <= 0 || r.X < 226 || r.X+r.W > float32(size[0]-20)+1 || r.Y < 20 || r.Y+r.H > float32(size[1]-20)+1 {
					t.Fatalf("warning pushed control outside content %v dark=%v: %s %+v", size, dark, label, r)
				}
			}
		}
	}
}

func TestNativeConversationLongMessagesKeepControlsWithinWindow(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	f.state.Voice.Turn = 1
	f.state.Voice.Transcript = strings.Repeat("这是一条需要换行的长消息。", 30)
	f.state.Voice.Response = strings.Repeat("我会为你详细解释。", 60)
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, label := range []string{"手动唤醒", "取消对话", "清空对话", "语音设置", "对话记录"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 || r.Y < 0 || r.Y+r.H > 600 {
			t.Fatalf("control outside minimum window: %s %+v", label, r)
		}
	}
	tt.Scroll(600, 300, 0, -300)
	r, _ := tt.Find("手动唤醒")
	if r.Y+r.H > 600 {
		t.Fatal("scroll moved conversation controls")
	}
}
