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
	for _, label := range []string{"通用", "屏幕", "助手", "语音", "智能家居"} {
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
	if a.page != "设置" || a.settingsTab != nativeSettingsVoiceTab {
		t.Fatal("voice settings shortcut did not select tab")
	}
}

func TestNativeAssistantAndVoiceSectionsKeepDraftSelectionAndSaveAcrossTabs(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 1600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "助手")
	if a.settingsTab != nativeSettingsAssistantTab || a.assistantSettingsSection != 0 {
		t.Fatalf("default assistant navigation: tab=%d section=%d", a.settingsTab, a.assistantSettingsSection)
	}
	nativeClick(t, tt, "语音")
	if a.settingsTab != nativeSettingsVoiceTab || a.voiceSettingsSection != 0 {
		t.Fatalf("default voice navigation: tab=%d section=%d", a.settingsTab, a.voiceSettingsSection)
	}
	groups := []struct {
		tab, title, field, value string
		section                  int
	}{
		{"助手", "基础设置", "角色设定", "分组中的角色草稿。", 0},
		{"助手", "对话服务", "对话模型", "section-model", 1},
		{"语音", "语音识别", "MiMo 服务地址", "https://asr.example/v1", 0},
		{"语音", "语音合成", "参考音频路径", "/tmp/section-reference.wav", 1},
		{"语音", "离线唤醒", "唤醒阈值", "0.68", 2},
		{"语音", "声音试听", "试听文本", "保留试听草稿。", 3},
	}
	assistantSelection, voiceSelection := 0, 0
	assertNavigation := func(tab string, section int) {
		t.Helper()
		for _, group := range groups {
			info, ok := tt.AccessByRole(ui.RoleButton, group.title)
			if group.tab != tab {
				if ok {
					t.Fatalf("%s category leaked into %s tab: %s", group.tab, tab, group.title)
				}
				continue
			}
			if !ok || (info.Selected || info.Checked) != (group.section == section) {
				t.Fatalf("navigation selection: %s %+v", group.title, info)
			}
		}
		for _, label := range []string{"连接", "通用", "屏幕", "助手", "语音", "智能家居"} {
			info, ok := tt.AccessByRole(ui.RoleTab, label)
			if !ok || info.Selected != (label == tab) {
				t.Fatalf("top-level settings tab changed: %s %+v", label, info)
			}
		}
		if a.assistantSettingsSection != assistantSelection || a.voiceSettingsSection != voiceSelection {
			t.Fatalf("tab switch changed independent selections: assistant=%d voice=%d, want %d/%d", a.assistantSettingsSection, a.voiceSettingsSection, assistantSelection, voiceSelection)
		}
	}
	for _, group := range groups {
		nativeClick(t, tt, group.tab)
		nativeClick(t, tt, group.title)
		if group.tab == "助手" {
			assistantSelection = group.section
		} else {
			voiceSelection = group.section
		}
		assertNavigation(group.tab, group.section)
		for _, other := range groups {
			_, rendered := tt.AccessByRole(ui.RoleTextField, other.field)
			if rendered != (other.title == group.title) {
				t.Fatalf("section %s rendered field %s: %v", group.title, other.field, rendered)
			}
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "保存设置"); !ok {
			t.Fatalf("missing unified save in %s", group.title)
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "保存语音设置"); ok {
			t.Fatal("obsolete bottom voice save remains")
		}
		nativeVoiceEdit(t, tt, group.field, group.value)
		tt.Key(0, ui.KeyTab)
	}
	before := a.draft.Voice
	if before.Persona != groups[0].value || before.LLMModel != groups[1].value || before.MiMoBaseURL != groups[2].value || before.ReferenceAudio != groups[3].value || before.WakeThreshold != 0.68 || a.voicePreview != groups[5].value || !a.dirty {
		t.Fatal("section navigation lost edited draft")
	}
	// Switching through unrelated tabs must restore each tab's last category,
	// not reset it or reinterpret the other tab's local section index.
	for _, tab := range []string{"连接", "通用", "屏幕", "智能家居", "助手", "语音", "助手"} {
		nativeClick(t, tt, tab)
		if tab == "助手" {
			assertNavigation(tab, assistantSelection)
		} else if tab == "语音" {
			assertNavigation(tab, voiceSelection)
		}
	}
	a.refresh()
	tt.Frame()
	for i := len(groups) - 1; i >= 0; i-- {
		group := groups[i]
		nativeClick(t, tt, group.tab)
		nativeClick(t, tt, group.title)
		if group.tab == "助手" {
			assistantSelection = group.section
		} else {
			voiceSelection = group.section
		}
		assertNavigation(group.tab, group.section)
		info, ok := tt.AccessByRole(ui.RoleTextField, group.field)
		if !ok || (group.field != "唤醒阈值" && info.Value != group.value) {
			t.Fatalf("section roundtrip lost editor %s: %+v", group.field, info)
		}
		if a.draft.Voice != before || a.voicePreview != groups[5].value {
			t.Fatal("section roundtrip replaced draft")
		}
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saves) != 1 || f.saves[0].Voice != before || a.dirty {
		t.Fatal("unified save did not persist assistant and voice groups")
	}
}

func TestNativeSettingsTopTabsUsableAtMinimumWindow(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, _, _, tt := nativeTest(t)
		tt.SetSize(760, 577)
		tt.SetDark(dark)
		nativeClick(t, tt, "设置")
		for _, dirty := range []bool{false, true} {
			if dirty {
				a.draft.Address = "compact-draft.local"
				a.edited()
				tt.Frame()
			}
			for index, label := range []string{"连接", "通用", "屏幕", "助手", "语音", "智能家居"} {
				info, ok := tt.AccessByRole(ui.RoleTab, label)
				r := info.Bounds
				if !ok || info.Disabled || r.W <= 0 || r.H <= 0 || r.X < 206 || r.X+r.W > 760 || r.Y < 0 || r.Y+r.H > 577 {
					t.Fatalf("tab outside minimum window (dark=%v dirty=%v): %s %+v", dark, dirty, label, info)
				}
				nativeClick(t, tt, label)
				info, ok = tt.AccessByRole(ui.RoleTab, label)
				if !ok || !info.Selected || a.settingsTab != index {
					t.Fatalf("tab not usable in expected order (dark=%v dirty=%v): %s tab=%d %+v", dark, dirty, label, a.settingsTab, info)
				}
				var previousRight float32
				for i, tab := range []string{"连接", "通用", "屏幕", "助手", "语音", "智能家居"} {
					other, found := tt.AccessByRole(ui.RoleTab, tab)
					if !found || other.Selected != (i == index) || (i > 0 && other.Bounds.X < previousRight) {
						t.Fatalf("tab order/selection overlaps: %s %+v", tab, other)
					}
					previousRight = other.Bounds.X + other.Bounds.W
				}
				save, ok := tt.AccessByRole(ui.RoleButton, "保存设置")
				if !ok || save.Bounds.X < previousRight || save.Bounds.W <= 0 || save.Bounds.X+save.Bounds.W > 760 || save.Bounds.Y < 0 || save.Bounds.Y+save.Bounds.H > 577 {
					t.Fatalf("save overlaps tabs or escapes minimum window (dark=%v dirty=%v): %+v", dark, dirty, save)
				}
			}
		}
	}
}

func TestNativeSettingsGroupDefinitionsAndBasicSliderIcon(t *testing.T) {
	if len(nativeAssistantSettingsGroups) != len(nativeAssistantSections) || len(nativeVoiceSettingsGroups) != len(nativeVoiceSections) {
		t.Fatal("assistant/voice category counts changed")
	}
	for i, title := range nativeAssistantSections {
		if nativeAssistantSettingsGroups[i].title != title {
			t.Fatalf("assistant category %d = %s, want %s", i, nativeAssistantSettingsGroups[i].title, title)
		}
	}
	for i, title := range nativeVoiceSections {
		if nativeVoiceSettingsGroups[i].title != title {
			t.Fatalf("voice category %d = %s, want %s", i, nativeVoiceSettingsGroups[i].title, title)
		}
	}
	if nativeAssistantSettingsGroups[0].icon != voiceSettingsIcon || nativeAssistantSettingsGroups[0].icon == sidebarSettings {
		t.Fatal("basic assistant settings must use the slider icon, not the sidebar gear")
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
	nativeClick(t, tt, "清空")
	nativeComplete(t, updates, tt)
	if len(a.voiceHistory) != 2 {
		t.Fatal("failed clear lost history")
	}
	f.commandError = nil
	nativeClick(t, tt, "清空对话")
	nativeClick(t, tt, "清空")
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
