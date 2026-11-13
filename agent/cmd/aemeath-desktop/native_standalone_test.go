package main

import (
	"aemeath/agent/internal/desktop"
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestNativeRunModeSelectorSavesDraftAndPreservesDevicePreferences(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1100, 800)
	a.draft.Address = "saved.local"
	a.draft.Voice.PlaybackOutput = "agent"
	nativeClick(t, tt, "设置")
	if radio, ok := tt.AccessByRole(ui.RoleRadio, "设备模式"); !ok || !radio.Checked {
		t.Fatal("legacy settings did not select device mode")
	}
	nativeClick(t, tt, "独立模式")
	if a.draft.RunMode != desktop.RunModeStandalone || !a.dirty || len(f.saves) != 0 {
		t.Fatal("mode selection did not remain an unsaved draft")
	}
	if address, ok := tt.AccessByRole(ui.RoleTextField, "设备地址"); !ok || !address.Disabled {
		t.Fatal("standalone draft did not disable device address")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].RunMode != desktop.RunModeStandalone || f.saves[0].Address != "saved.local" || f.saves[0].Voice.PlaybackOutput != "agent" {
		t.Fatalf("mode save changed device preferences: %+v", f.saves)
	}
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "声音试听")
	for _, label := range []string{"Agent", "监控台"} {
		info, ok := tt.AccessByRole(ui.RoleRadio, label)
		if !ok || !info.Disabled {
			t.Fatalf("standalone playback route enabled: %s %+v", label, info)
		}
	}
	if !tt.HasText("独立模式始终使用本机麦克风和扬声器；设备模式的播放位置会保留。") {
		t.Fatal("missing effective playback explanation")
	}
	nativeClick(t, tt, "连接")
	nativeClick(t, tt, "设备模式")
	if address, ok := tt.AccessByRole(ui.RoleTextField, "设备地址"); !ok || address.Disabled || a.draft.Address != "saved.local" {
		t.Fatal("returning to device mode lost address")
	}
}

func TestNativeStandaloneStatusAndVoiceControlsIgnoreDeviceConnection(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	a.service = &nativeVoiceFake{nativeFake: f}
	nativeClick(t, tt, "语音助手")
	f.state.Settings.RunMode = desktop.RunModeStandalone
	f.state.Settings.Voice.Enabled = true
	f.state.Connected = false
	f.state.Running = true
	f.state.Status = "standalone"
	f.state.Voice.Connected = true
	f.state.Voice.Phase = "idle"
	a.refresh()
	tt.Frame()
	if !tt.HasText("独立模式 · 就绪") || tt.HasText("设备未连接") {
		t.Fatal("standalone sidebar depends on device status")
	}
	if !tt.HasText("语音路径：本机麦克风 → 本机扬声器") {
		t.Fatal("missing standalone voice path")
	}
	if a.voiceDisabled() {
		t.Fatal("usable standalone voice disabled with offline ESP32")
	}
	f.state.Voice.Phase = "speaking"
	a.refresh()
	tt.Frame()
	if !tt.HasText("语音路径：本机麦克风 → 本机扬声器 · 播放时暂停自动唤醒，可手动打断") {
		t.Fatal("missing automatic wake pause hint")
	}
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "屏幕")
	if screen, ok := tt.AccessByRole(ui.RoleSwitch, "亮屏"); !ok || !screen.Disabled {
		t.Fatal("standalone enabled ESP32 screen controls")
	}
	f.state.Voice.Connected = false
	f.state.Voice.Error = "microphone unavailable"
	a.refresh()
	tt.Frame()
	if !tt.HasText("独立模式 · 语音异常") || !a.voiceDisabled() {
		t.Fatal("voice failure did not change standalone status and controls")
	}
	f.state.Running = false
	a.refresh()
	tt.Frame()
	if !tt.HasText("独立模式 · 已停止") {
		t.Fatal("missing stopped standalone status")
	}
}
