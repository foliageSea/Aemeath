package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func nativeVoiceClickRole(t *testing.T, tt *ui.Tester, role ui.Role, label string) {
	t.Helper()
	info, ok := tt.AccessByRole(role, label)
	if !ok {
		t.Fatalf("missing control: %s", label)
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
	nativeClick(t, tt, "语音助手")
	fields := []struct{ label, value string }{
		{"OpenAI 服务地址", "https://chat.example/v1"},
		{"OpenAI API 密钥", "private-openai-key"},
		{"对话模型", "chat-model"},
		{"MiMo 服务地址", "https://voice.example/v1"},
		{"MiMo API 密钥", "private-mimo-key"},
		{"参考音频路径", "/tmp/voice/reference.wav"},
		{"唤醒模型目录", "/tmp/voice/models"},
		{"角色设定", "温柔、简短地回答。"},
	}
	for _, field := range fields {
		nativeVoiceEdit(t, tt, field.label, field.value)
	}
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "启用语音助手")
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "允许查询监控与控制设备")
	nativeClick(t, tt, "唤醒阈值")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("0.65")
	tt.Key(0, ui.KeyTab)
	nativeVoiceClickRole(t, tt, ui.RoleSlider, "播放音量")
	if !a.draft.Voice.Enabled || !a.draft.Voice.LLMTools || a.draft.Voice.WakeThreshold != 0.65 || a.draft.Voice.Volume == 0 {
		t.Fatalf("switches, threshold or volume not bound: %+v", a.draft.Voice)
	}
	for _, label := range []string{"OpenAI API 密钥", "MiMo API 密钥"} {
		info, ok := tt.AccessByRole(ui.RoleTextField, label)
		if !ok || !info.Password || strings.Contains(info.Value, "private-") {
			t.Fatalf("secret exposed through accessibility: %+v", info)
		}
	}
	before := a.draft.Voice
	gotFields := []string{before.LLMBaseURL, before.LLMAPIKey, before.LLMModel, before.MiMoBaseURL, before.MiMoAPIKey, before.ReferenceAudio, before.WakeModelDir, before.Persona}
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
	nativeClick(t, tt, "保存语音设置")
	nativeComplete(t, updates, tt)
	if a.dirty {
		t.Fatal("latest voice draft remained dirty after save")
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
	if !tt.HasText("离线唤醒：未就绪（模型缺失或不可用）") || !tt.HasText("语音提示：找不到唤醒模型") {
		t.Fatal("missing models were not diagnosed")
	}
	nativeVoiceEdit(t, tt, "试听文本", "你好，爱弥斯。")
	for _, label := range []string{"手动唤醒", "取消对话", "清空对话", "试听语音", "播放参考音频"} {
		info, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || info.Disabled {
			t.Fatalf("manual diagnosis command disabled without models: %s", label)
		}
		nativeClick(t, tt, label)
		nativeComplete(t, updates, tt)
	}
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
	a.refresh()
	tt.Frame()
	for _, text := range []string{"离线唤醒：已就绪 · 请说「你好小爱」", "语音通道：已连接 · 正在说话", "你说：现在电脑忙吗？", "爱弥斯：我来帮你看看。", "第 3 轮 · 识别 12 ms · 思考 34 ms · 语音 56 ms"} {
		if !tt.HasText(text) {
			t.Fatalf("updated voice state not displayed: %s", text)
		}
	}
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

func TestNativeVoiceOfflineAndUnsupportedControls(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Settings.Voice.Enabled = true
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, label := range []string{"手动唤醒", "取消对话", "清空对话", "试听语音", "播放参考音频"} {
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
	if !info.Disabled || !tt.HasText("语音助手尚未启用，填写设置后保存即可开始。") {
		t.Fatal("disabled voice assistant state not shown")
	}
}

func TestNativeVoiceMinimumWindowAndScrollableSettings(t *testing.T) {
	a, _, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "语音助手")
	for _, label := range []string{"语音屏", "手动唤醒", "试听文本", "播放参考音频"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 || r.Y+r.H > 600 {
			t.Fatalf("voice control outside minimum window: %s %+v", label, r)
		}
	}
	tt.Scroll(600, 400, 0, 4000)
	info, ok := tt.AccessByRole(ui.RoleTextField, "角色设定")
	if !ok || info.Bounds.W <= 0 || info.Bounds.X < 206 || info.Bounds.X+info.Bounds.W > 760 || info.Bounds.Y+info.Bounds.H > 600 {
		t.Fatalf("voice settings cannot scroll into view: %+v", info)
	}
	nativeVoiceEdit(t, tt, "角色设定", "保持简洁。")
	if a.draft.Voice.Persona != "保持简洁。" || !a.dirty {
		t.Fatal("scrolled voice settings not editable")
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
