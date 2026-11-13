package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

var nativeAssistantSections = [...]string{"基础设置", "对话服务", "记忆"}
var nativeVoiceSections = [...]string{"语音识别", "语音合成", "离线唤醒", "声音试听"}

func nativeVoiceNavigate(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	section := ""
	switch label {
	case "启用语音助手", "角色设定":
		section = "基础设置"
	case "OpenAI 服务地址", "OpenAI API 密钥", "Tavily API 密钥", "对话模型", "启用 Agent 工具",
		"自动读取模型上下文容量", "备用上下文容量（Tokens）", "手动上下文容量（Tokens）",
		"使用 models.dev 查询上下文容量", "models.dev 服务商 ID", "models.dev 模型 ID":
		section = "对话服务"
	case "MiMo 服务地址", "MiMo API 密钥":
		section = "语音识别"
	case "MiMo", "Fish", "Fish 服务地址", "Fish API 密钥", "Fish 模型", "Fish reference_id", "Fish 参考音频转写", "参考音频路径", "低延迟":
		section = "语音合成"
	case "唤醒词", "唤醒模型目录", "唤醒阈值", "模型目录与高级选项", "安装唤醒模型":
		section = "离线唤醒"
	case "播放音量", "试听文本", "试听语音", "播放参考音频":
		section = "声音试听"
	}
	if section != "" {
		tab := "语音"
		if section == "基础设置" || section == "对话服务" {
			tab = "助手"
		}
		if info, ok := tt.AccessByRole(ui.RoleTab, tab); !ok || !info.Selected {
			nativeClick(t, tt, tab)
		}
		nativeClick(t, tt, section)
	}
	if label == "唤醒模型目录" {
		if _, ok := tt.AccessByRole(ui.RoleTextField, label); !ok {
			nativeClick(t, tt, "模型目录与高级选项")
		}
	}
}

func nativeVoiceClickRole(t *testing.T, tt *ui.Tester, role ui.Role, label string) {
	t.Helper()
	info, ok := tt.AccessByRole(role, label)
	if !ok {
		nativeVoiceNavigate(t, tt, label)
		info, ok = tt.AccessByRole(role, label)
	}
	if !ok {
		t.Fatalf("missing control: %s", label)
	}
	for _, title := range []string{"基础设置", "对话服务", "记忆", "语音识别", "语音合成", "离线唤醒", "声音试听"} {
		viewport, found := tt.Find(title + "设置内容")
		if !found {
			continue
		}
		r := info.Bounds
		dy := float32(0)
		if r.Y < viewport.Y+8 {
			dy = r.Y - viewport.Y - 8
		} else if r.Y+r.H > viewport.Y+viewport.H-8 {
			dy = r.Y + r.H - viewport.Y - viewport.H + 8
		}
		if dy != 0 {
			tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, dy)
			info, ok = tt.AccessByRole(role, label)
			if !ok || info.Bounds.Y < viewport.Y || info.Bounds.Y+info.Bounds.H > viewport.Y+viewport.H {
				t.Fatalf("control did not scroll into view: %s %+v", label, info)
			}
		}
		break
	}
	r := info.Bounds
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
}

func nativeVoiceEdit(t *testing.T, tt *ui.Tester, label, value string) {
	t.Helper()
	nativeVoiceClickRole(t, tt, ui.RoleTextField, label)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(value)
}

func TestNativeVoiceSettingsMaskedAndSavedWithoutLosingNewEdits(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 2600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	fields := []struct{ label, value string }{
		{"OpenAI 服务地址", "https://chat.example/v1"},
		{"OpenAI API 密钥", "private-openai-key"},
		{"对话模型", "chat-model"},
		{"MiMo 服务地址", "https://voice.example/v1"},
		{"MiMo API 密钥", "private-mimo-key"},
		{"参考音频路径", "/tmp/voice/reference.wav"},
		{"唤醒模型目录", "/tmp/voice/models"},
		{"角色设定", "温柔、简短地回答。"},
		{"唤醒词", "你好爱弥斯"},
	}
	for _, field := range fields {
		nativeVoiceEdit(t, tt, field.label, field.value)
	}
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "启用语音助手")
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "启用 Agent 工具")
	nativeVoiceNavigate(t, tt, "唤醒阈值")
	nativeClick(t, tt, "唤醒阈值")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("0.65")
	tt.Key(0, ui.KeyTab)
	nativeVoiceClickRole(t, tt, ui.RoleSlider, "播放音量")
	if !a.draft.Voice.Enabled || !a.draft.Voice.LLMTools || a.draft.Voice.WakeThreshold != 0.65 || a.draft.Voice.Volume == 0 {
		t.Fatalf("switches, threshold or volume not bound: %+v", a.draft.Voice)
	}
	for _, label := range []string{"OpenAI API 密钥", "MiMo API 密钥"} {
		nativeVoiceNavigate(t, tt, label)
		info, ok := tt.AccessByRole(ui.RoleTextField, label)
		if !ok || !info.Password || strings.Contains(info.Value, "private-") {
			t.Fatalf("secret exposed through accessibility: %+v", info)
		}
	}
	before := a.draft.Voice
	gotFields := []string{before.LLMBaseURL, before.LLMAPIKey, before.LLMModel, before.MiMoBaseURL, before.MiMoAPIKey, before.ReferenceAudio, before.WakeModelDir, before.Persona, before.WakePhrase}
	for i, field := range fields {
		if gotFields[i] != field.value {
			t.Fatalf("%s input was not bound", field.label)
		}
	}
	a.refresh()
	tt.Frame()
	if a.draft.Voice != before {
		t.Fatal("snapshot replaced voice draft")
	}
	gate := make(chan struct{})
	f.gate = gate
	nativeClick(t, tt, "保存设置")
	nativeVoiceEdit(t, tt, "对话模型", "newer-model")
	close(gate)
	nativeComplete(t, updates, tt)
	if !a.dirty || a.draft.Voice.LLMModel != "newer-model" {
		t.Fatal("pending save discarded later voice edit")
	}
	f.mu.Lock()
	saved := f.saves[0].Voice
	f.mu.Unlock()
	if saved != before {
		t.Fatalf("save did not capture the full voice configuration: got %+v want %+v", saved, before)
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if a.dirty {
		t.Fatal("latest voice draft remained dirty after save")
	}
}

func TestNativeWakePhraseEditMarksDirtyAndPersists(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 1000)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeVoiceEdit(t, tt, "唤醒词", "你好爱弥斯")
	if !a.dirty || a.draft.Voice.WakePhrase != "你好爱弥斯" {
		t.Fatal("wake phrase edit did not update the draft and dirty state")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saves) != 1 || f.saves[0].Voice.WakePhrase != "你好爱弥斯" || a.dirty {
		t.Fatal("wake phrase was not saved through the usual settings path")
	}
}

func nativeVoiceAssertTooltip(t *testing.T, tt *ui.Tester, label, want string) {
	t.Helper()
	info, ok := tt.Access(label)
	if !ok || info.Description != want {
		t.Fatalf("tooltip for %s = %q (found=%v), want %q", label, info.Description, ok, want)
	}
}

func TestNativeVoiceCompactHeaderKeepsTwoRows(t *testing.T) {
	for _, size := range [][2]int{{760, 577}, {1100, 900}, {1762, 883}} {
		for _, dark := range []bool{false, true} {
			a, f, _, tt := nativeTest(t)
			a.service = &nativeVoiceFake{nativeFake: f}
			tt.SetSize(size[0], size[1])
			tt.SetDark(dark)
			a.voiceGlobalReady, a.voiceGlobalKey = true, "F8"
			nativeClick(t, tt, "语音助手")
			for _, state := range []struct {
				enabled, ready bool
				want, detail   string
			}{
				{false, false, "助手未启用", "语音助手尚未启用，填写设置后保存即可开始。"},
				{true, false, "唤醒未就绪", "离线唤醒：未就绪（模型缺失或不可用）"},
				{true, true, "唤醒已就绪", "离线唤醒：已就绪 · 请说「你好爱弥斯今天也请多关照」"},
			} {
				f.state.Settings.Voice.Enabled = state.enabled
				f.state.Settings.Voice.WakePhrase = "你好爱弥斯今天也请多关照"
				f.state.Voice.WakeReady = state.ready
				a.refresh()
				tt.Frame()
				header, ok := tt.Find("语音助手状态栏")
				if !ok {
					t.Fatal("missing assistant header")
				}
				first, ok := tt.Find("助手连接状态")
				if !ok {
					t.Fatal("missing first header row")
				}
				second, ok := tt.Find("助手唤醒与快捷键")
				if !ok || first.Y+first.H > second.Y+1 || second.Y+second.H > header.Y+header.H+1 || header.H > 60 {
					t.Fatalf("header is not compact two-row layout: size=%v dark=%v header=%+v first=%+v second=%+v", size, dark, header, first, second)
				}
				for _, label := range []string{"离线唤醒状态", state.want, "语音快捷键提示"} {
					box, found := tt.Find(label)
					if !found || box.W <= 0 || box.H <= 0 || box.X < second.X || box.X+box.W > second.X+second.W+1 || box.Y < second.Y || box.Y+box.H > second.Y+second.H+1 {
						t.Fatalf("second-row item escaped header: %s %+v second=%+v", label, box, second)
					}
				}
				status, _ := tt.Find("离线唤醒状态")
				hint, _ := tt.Find("语音快捷键提示")
				if status.X+status.W >= hint.X {
					t.Fatalf("wake and shortcut overlap: status=%+v hint=%+v", status, hint)
				}
				if !tt.HasText("F8 按住说话") || tt.HasText(state.detail) {
					t.Fatal("header did not abbreviate status and shortcut")
				}
				nativeVoiceAssertTooltip(t, tt, "离线唤醒状态", state.detail)
				nativeVoiceAssertTooltip(t, tt, "语音快捷键提示", "全局 F8 · 后台也可按住说话 · 松开发送 · 最长 30 秒")
				controls, ok := tt.Find("语音操作栏")
				if !ok || header.Y+header.H > controls.Y || controls.Y+controls.H > float32(size[1])+1 {
					t.Fatalf("header or controls escaped window: header=%+v controls=%+v", header, controls)
				}
				count := 0
				for _, text := range tt.Texts() {
					if text == state.want {
						count++
					}
				}
				wantCount := 1
				if !state.enabled {
					// Both the wake header and composer footer explain the disabled assistant.
					wantCount = 2
				}
				if count != wantCount {
					t.Fatalf("wake status rendered %d times, want %d", count, wantCount)
				}
			}
		}
	}
}

func TestNativeVoiceCompactConnectionState(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 577)
	nativeClick(t, tt, "语音助手")
	for _, state := range []struct {
		connected           bool
		phase, want, detail string
	}{
		{false, "idle", "· 未连接", "语音通道：未连接"},
		{false, "speaking", "· 未连接", "语音通道：未连接"},
		{true, "idle", "· 已连接 · 待命", "语音通道：已连接 · 待命"},
		{true, "speaking", "· 已连接 · 正在说话", "语音通道：已连接 · 正在说话"},
	} {
		f.state.Voice.Connected, f.state.Voice.Phase = state.connected, state.phase
		a.refresh()
		tt.Frame()
		if !tt.HasText(state.want) {
			t.Fatalf("missing compact connection state: %s", state.want)
		}
		nativeVoiceAssertTooltip(t, tt, state.want, state.detail)
		row, _ := tt.Find("助手连接状态")
		text, ok := tt.Find(state.want)
		if !ok || text.X < row.X || text.X+text.W > row.X+row.W+1 || text.Y < row.Y || text.Y+text.H > row.Y+row.H+1 {
			t.Fatal("connection status escaped first row")
		}
		if !state.connected {
			for _, text := range tt.Texts() {
				if strings.Contains(text, "未连接") && strings.Contains(text, "待命") {
					t.Fatalf("disconnected header still shows standby: %s", text)
				}
			}
		}
	}
}

func TestNativeVoiceCompactShortcutDynamicState(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	a.service = &nativeVoiceFake{nativeFake: f}
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	a.refresh()
	tt.SetSize(760, 577)
	nativeClick(t, tt, "语音助手")
	for _, state := range []struct {
		ready, hold       bool
		key, want, detail string
	}{
		{false, false, "", "快捷键未就绪", "全局快捷键尚未就绪"},
		{true, false, "", "F8 按住说话", "全局 F8 · 后台也可按住说话 · 松开发送 · 最长 30 秒"},
		{true, false, "Ctrl+Shift+F8", "Ctrl+Shift+F8 按住说话", "全局 Ctrl+Shift+F8 · 后台也可按住说话 · 松开发送 · 最长 30 秒"},
		{true, true, "Ctrl+Shift+F8", "录音中 · 松开发送", "正在按住录音 · 松开 Ctrl+Shift+F8 发送 · 前台 Esc 取消"},
		{true, false, "F8", "F8 按住说话", "全局 F8 · 后台也可按住说话 · 松开发送 · 最长 30 秒"},
	} {
		a.voiceGlobalReady, a.voiceHold, a.voiceGlobalKey = state.ready, state.hold, state.key
		tt.Frame()
		if !tt.HasText(state.want) || tt.HasText(state.detail) {
			t.Fatalf("shortcut did not abbreviate dynamic state: %s", state.want)
		}
		nativeVoiceAssertTooltip(t, tt, "语音快捷键提示", state.detail)
		row, _ := tt.Find("助手唤醒与快捷键")
		hint, ok := tt.Find("语音快捷键提示")
		if !ok || hint.X < row.X || hint.X+hint.W > row.X+row.W+1 || hint.Y < row.Y || hint.Y+hint.H > row.Y+row.H+1 {
			t.Fatalf("dynamic shortcut escaped second row: hint=%+v row=%+v", hint, row)
		}
	}
}

func TestNativeVoiceWakeHintsUseSavedPhrase(t *testing.T) {
	for _, test := range []struct {
		name, saved, want string
	}{
		{"custom", "你好爱弥斯", "你好爱弥斯"},
		{"trimmed", " 你好爱弥斯 ", "你好爱弥斯"},
		{"empty default", "", "你好小爱"},
		{"blank default", " \t ", "你好小爱"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, f, _, tt := nativeTest(t)
			a.service = &nativeVoiceFake{nativeFake: f}
			f.state.Settings.Voice.Enabled = true
			f.state.Settings.Voice.WakePhrase = test.saved
			f.state.Voice.Connected, f.state.Voice.WakeReady = true, true
			a.refresh()
			a.draft.Voice.WakePhrase = "尚未保存"
			a.edited()
			nativeClick(t, tt, "语音助手")
			for _, text := range []string{
				"说出「" + test.want + "」，或点击手动唤醒开始对话。",
				"唤醒已就绪",
			} {
				if !tt.HasText(text) {
					t.Fatalf("missing saved wake phrase hint: %s", text)
				}
			}
			nativeVoiceAssertTooltip(t, tt, "离线唤醒状态", "离线唤醒：已就绪 · 请说「"+test.want+"」")
		})
	}
}

type nativeVoiceFake struct {
	*nativeFake
	commands     [][2]string
	commandError error
}

func (f *nativeVoiceFake) VoiceCommand(kind, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, [2]string{kind, text})
	return f.commandError
}

func TestNativeVoiceCommandsRemainAvailableWithoutWakeModels(t *testing.T) {
	a, base, updates, tt := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.Error = "找不到唤醒模型"
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if !tt.HasText("唤醒未就绪") || !tt.HasText("语音提示：找不到唤醒模型") {
		t.Fatal("missing models were not diagnosed")
	}
	tt.SetSize(1100, 3200)
	for _, label := range []string{"手动唤醒", "取消对话", "清空对话"} {
		if label == "取消对话" {
			f.state.Voice.Phase = "listening"
			a.refresh()
			tt.Frame()
		}
		info, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || info.Disabled {
			t.Fatalf("manual diagnosis command disabled without models: %s", label)
		}
		nativeClick(t, tt, label)
		if label == "清空对话" {
			nativeClick(t, tt, "清空")
		}
		nativeComplete(t, updates, tt)
	}
	nativeClick(t, tt, "语音设置")
	nativeClick(t, tt, "声音试听")
	nativeVoiceEdit(t, tt, "试听文本", "你好，爱弥斯。")
	for _, label := range []string{"试听语音", "播放参考音频"} {
		nativeClick(t, tt, label)
		nativeComplete(t, updates, tt)
	}
	nativeClick(t, tt, "语音助手")
	want := [][2]string{{"wake", ""}, {"cancel", ""}, {"clear", ""}, {"preview", "你好，爱弥斯。"}, {"reference", ""}}
	f.mu.Lock()
	got := append([][2]string(nil), f.commands...)
	f.mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	f.state.Voice.WakeReady = true
	f.state.Voice.Error = ""
	f.state.Voice.Phase = "speaking"
	f.state.Voice.Transcript = "现在电脑忙吗？"
	f.state.Voice.Response = "我来帮你看看。"
	f.state.Voice.Turn = 3
	f.state.Voice.ASRMillis, f.state.Voice.AgentMillis, f.state.Voice.TTSMillis = 12, 34, 56
	f.state.Voice.FirstAudioMillis = 789
	f.state.Voice.GeneratedSegments, f.state.Voice.PlayedSegments = 3, 1
	a.refresh()
	tt.Frame()
	for _, text := range []string{"唤醒已就绪", "· 已连接 · 正在说话", "现在电脑忙吗？", "我来帮你看看。"} {
		if !tt.HasText(text) {
			t.Fatalf("updated voice state not displayed: %s", text)
		}
	}
	nativeClick(t, tt, "语音设置")
	nativeClick(t, tt, "声音试听")
	for _, text := range []string{"第 3 轮 · 识别 12 ms · 思考 34 ms · 合成累计 56 ms", "首段播放 789 ms · 已合成 3 段 · 播放完成 1 段"} {
		if !tt.HasText(text) {
			t.Fatalf("missing audio diagnostics: %s", text)
		}
	}
	nativeClick(t, tt, "语音助手")
	if a.voicePreview != "你好，爱弥斯。" {
		t.Fatal("state update replaced preview editor")
	}
	f.commandError = errors.New("设备忙，请稍后重试")
	a.draft.Voice.LLMModel = "unfinished"
	a.edited()
	nativeClick(t, tt, "手动唤醒")
	nativeComplete(t, updates, tt)
	if !strings.Contains(a.notice, "设备忙") || !a.dirty || a.draft.Voice.LLMModel != "unfinished" {
		t.Fatal("command failure lost feedback or draft")
	}
}

func TestNativeVoiceCancelVisibilityFollowsPhase(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	a.service = &nativeVoiceFake{nativeFake: f}
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	nativeClick(t, tt, "语音助手")
	for _, phase := range []string{"", "idle", "wake", "listening", "recognizing", "thinking", "synthesizing", "speaking", "error", "idle"} {
		f.state.Voice.Phase = phase
		a.refresh()
		tt.Frame()
		want := phase != "" && phase != "idle" && phase != "error"
		button, found := tt.AccessByRole(ui.RoleButton, "取消对话")
		if found != want || (found && button.Disabled) {
			t.Fatalf("phase %q cancel visibility/enabled = %v/%v, want visible=%v", phase, found, !button.Disabled, want)
		}
	}
}

func TestNativeRetryOnlySynthesizesSavedReply(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	voiceService := &nativeVoiceFake{nativeFake: f}
	a.service = voiceService
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.Phase = "error"
	f.state.Voice.Response = "这条回复已经完成，只重新合成声音。"
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if _, ok := tt.AccessByRole(ui.RoleButton, "取消对话"); ok {
		t.Fatal("error composer exposes cancel alongside retry")
	}
	nativeClick(t, tt, "重试回复语音")
	nativeComplete(t, updates, tt)
	voiceService.mu.Lock()
	defer voiceService.mu.Unlock()
	if len(voiceService.commands) != 1 || voiceService.commands[0] != [2]string{"preview", f.state.Voice.Response} {
		t.Fatalf("retry commands=%v", voiceService.commands)
	}
}

func TestNativeListeningNoticeIsNotAnError(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	a.service = &nativeVoiceFake{nativeFake: f}
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.WakeReady = true
	f.state.Voice.Phase = "listening"
	f.state.Voice.Notice = "我在听，请继续说出问题"
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if !tt.HasText(f.state.Voice.Notice) {
		t.Fatal("missing normal listening notice")
	}
	if tt.HasText("需要处理") || tt.HasText("语音提示："+f.state.Voice.Notice) {
		t.Fatal("normal notice displayed as error")
	}
}

func TestNativeVoiceOfflineAndUnsupportedControls(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Phase = "listening"
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, label := range []string{"手动唤醒", "取消对话", "清空对话"} {
		info, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || !info.Disabled {
			t.Fatalf("unsupported control not disabled: %s", label)
		}
	}
	if !tt.HasText("当前服务不支持语音操作，请更新桌面应用。") {
		t.Fatal("unsupported host has no explanation")
	}
	a.service = &nativeVoiceFake{nativeFake: f}
	tt.Frame()
	info, _ := tt.AccessByRole(ui.RoleButton, "手动唤醒")
	if !info.Disabled {
		t.Fatal("offline voice control enabled")
	}
	f.state.Voice.Connected = true
	f.state.Settings.Voice.Enabled = false
	a.refresh()
	tt.Frame()
	info, _ = tt.AccessByRole(ui.RoleButton, "手动唤醒")
	if !info.Disabled || !tt.HasText("助手未启用") {
		t.Fatal("disabled voice assistant state not shown")
	}
}

func TestNativeVoiceMinimumWindowAndScrollableSettings(t *testing.T) {
	a, _, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "语音助手")
	if _, ok := tt.AccessByRole(ui.RoleButton, "取消对话"); ok {
		t.Fatal("idle composer exposes cancel")
	}
	for _, label := range []string{"语音屏", "手动唤醒", "语音设置"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 || r.Y+r.H > 600 {
			t.Fatalf("voice control outside minimum window: %s %+v", label, r)
		}
	}
	nativeClick(t, tt, "语音设置")
	if a.settingsTab != nativeSettingsVoiceTab {
		t.Fatal("voice shortcut did not open voice tab")
	}
	nativeVoiceNavigate(t, tt, "角色设定")
	info, ok := tt.AccessByRole(ui.RoleTextField, "角色设定")
	if !ok || info.Bounds.W <= 0 || info.Bounds.X < 206 || info.Bounds.X+info.Bounds.W > 760 || info.Bounds.Y+info.Bounds.H > 600 {
		t.Fatalf("voice settings cannot scroll into view: %+v", info)
	}
	nativeVoiceEdit(t, tt, "角色设定", "保持简洁。")
	if a.draft.Voice.Persona != "保持简洁。" || !a.dirty {
		t.Fatal("basic voice settings not editable")
	}
	nativeVoiceNavigate(t, tt, "Tavily API 密钥")
	tt.Scroll(650, 400, 0, 4000)
	if a.assistantSettingsScroll[1].Y <= 0 {
		t.Fatal("assistant conversation settings did not scroll")
	}
	nativeVoiceEdit(t, tt, "Tavily API 密钥", "retained-assistant-key")
	tt.Key(0, ui.KeyTab)
	conversationY := a.assistantSettingsScroll[1].Y
	nativeClick(t, tt, "语音")
	for _, test := range []struct {
		section      int
		label, value string
	}{
		{1, "参考音频路径", "/tmp/scrolled-reference.wav"},
		{2, "唤醒阈值", "0.72"},
	} {
		if test.section == 2 {
			// A wrapped installation error must not hide the editable wake fields.
			a.wakeAssets.path = a.wakeModelPath()
			a.wakeAssets.checked = true
			a.wakeAssets.err = errors.New(strings.Repeat("下载失败，请检查网络连接。", 8))
		}
		nativeClick(t, tt, nativeVoiceSections[test.section])
		if test.section == 1 {
			nativeClick(t, tt, "Fish")
		} else {
			nativeClick(t, tt, "模型目录与高级选项")
		}
		tt.Scroll(650, 400, 0, 4000)
		if a.voiceSettingsScroll[test.section].Y <= 0 {
			t.Fatalf("section did not scroll: %s", nativeVoiceSections[test.section])
		}
		info, ok := tt.AccessByRole(ui.RoleTextField, test.label)
		if !ok || info.Bounds.W <= 0 || info.Bounds.Y < 0 || info.Bounds.Y+info.Bounds.H > 600 {
			t.Fatalf("scrolled field outside minimum window: %s %+v", test.label, info)
		}
		nativeVoiceEdit(t, tt, test.label, test.value)
		tt.Key(0, ui.KeyTab)
		if save, ok := tt.AccessByRole(ui.RoleButton, "保存设置"); !ok || save.Bounds.Y < 0 || save.Bounds.Y+save.Bounds.H > 600 {
			t.Fatalf("save moved outside window after scrolling: %+v", save)
		}
	}
	synthesisY, wakeY := a.voiceSettingsScroll[1].Y, a.voiceSettingsScroll[2].Y
	nativeClick(t, tt, "助手")
	if a.assistantSettingsSection != 1 || a.assistantSettingsScroll[1].Y != conversationY {
		t.Fatal("assistant tab lost conversation selection or scroll position")
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Tavily API 密钥"); !ok {
		t.Fatal("assistant tab did not restore scrolled conversation editor")
	}
	nativeClick(t, tt, "基础设置")
	nativeClick(t, tt, "对话服务")
	if a.assistantSettingsScroll[1].Y != conversationY {
		t.Fatal("assistant category roundtrip lost its scroll position")
	}
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "语音")
	if a.voiceSettingsSection != 2 || a.voiceSettingsScroll[2].Y != wakeY {
		t.Fatal("voice tab lost wake selection or scroll position after visiting assistant settings")
	}
	nativeClick(t, tt, "语音合成")
	if a.voiceSettingsScroll[1].Y != synthesisY {
		t.Fatal("synthesis section lost its scroll position")
	}
	nativeClick(t, tt, "离线唤醒")
	if a.voiceSettingsScroll[2].Y != wakeY {
		t.Fatal("wake section lost its scroll position")
	}
	if a.draft.Voice.Persona != "保持简洁。" || a.draft.Voice.TavilyAPIKey != "retained-assistant-key" || a.draft.Voice.ReferenceAudio != "/tmp/scrolled-reference.wav" || a.draft.Voice.WakeThreshold != 0.72 || !a.dirty {
		t.Fatal("scrolled synthesis or wake settings not editable")
	}
}

func TestNativeVoiceDevicePageSwitch(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	nativeClick(t, tt, "语音屏")
	if len(f.pages) != 0 {
		t.Fatal("offline voice page switch sent command")
	}
	f.state.Connected = true
	a.refresh()
	tt.Frame()
	nativeClick(t, tt, "语音屏")
	nativeComplete(t, updates, tt)
	if len(f.pages) != 1 || f.pages[0] != "voice" {
		t.Fatalf("voice page not dispatched: %v", f.pages)
	}
}
