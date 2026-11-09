package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// Optional so hosts without voice support can still show and edit settings.
type nativeVoiceService interface {
	VoiceCommand(kind, text string) error
}

func nativeVoicePhase(phase string) string {
	switch phase {
	case "wake":
		return "已唤醒"
	case "listening":
		return "正在聆听"
	case "recognizing":
		return "正在识别"
	case "thinking":
		return "正在思考"
	case "synthesizing":
		return "正在生成语音"
	case "speaking":
		return "正在说话"
	case "error":
		return "需要处理"
	case "connecting":
		return "正在连接"
	case "disabled":
		return "已停用"
	default:
		return "待命"
	}
}

func (a *nativeUI) voiceCommand(kind, text string) {
	service, ok := a.service.(nativeVoiceService)
	if !ok {
		a.notify("当前服务不支持语音操作，请更新桌面应用。")
		return
	}
	a.run("voice-command", func() error { return service.VoiceCommand(kind, text) }, func() { a.notify("语音指令已发送。") })
}

func (a *nativeUI) voiceView(c *ui.Context) {
	a.voiceStatusView(c)
	a.voiceSettingsView(c)
}

func (a *nativeUI) voiceStatusView(c *ui.Context) {
	t := c.Theme()
	state := a.snapshot.Voice
	_, supported := a.service.(nativeVoiceService)
	disabled := !supported || !a.snapshot.Settings.Voice.Enabled || !state.Connected || a.busy["voice-command"]
	ui.Column(c.Key("voice-status")).FillWidth().Padding(20).Gap(12).Radius(18).Background(t.Surface).Children(func() {
		ui.Text(c, "对话与试听").Bold().FontSize(17)
		if !supported {
			ui.Text(c, "当前服务不支持语音操作，请更新桌面应用。").FontSize(12).TextColor(t.Warning).Wrap()
		}
		connection := "语音通道：未连接"
		if state.Connected {
			connection = "语音通道：已连接"
		}
		ui.Text(c, connection+" · "+nativeVoicePhase(state.Phase)).Wrap()
		wake := "离线唤醒：未就绪（模型缺失或不可用）"
		color := t.Warning
		if !a.snapshot.Settings.Voice.Enabled {
			wake, color = "语音助手尚未启用，填写设置后保存即可开始。", t.TextMuted
		} else if state.WakeReady {
			wake, color = "离线唤醒：已就绪 · 请说「你好小爱」", t.Success
		}
		ui.Text(c, wake).FontSize(12).TextColor(color).Wrap()
		if a.snapshot.Settings.Voice.Enabled && !state.WakeReady {
			ui.Text(c, "请安装或检查唤醒模型；连接后仍可手动唤醒、试听语音和播放参考音频。").FontSize(12).TextColor(t.TextMuted).Wrap()
		}
		if state.Notice != "" {
			ui.Text(c, state.Notice).FontSize(12).TextColor(t.Text).Wrap()
		}
		if state.Error != "" {
			ui.Text(c, "语音提示："+state.Error).FontSize(12).TextColor(t.Warning).Wrap().Selectable()
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c.Key("voice-wake"), "手动唤醒").Disabled(disabled).OnClick(func() { a.voiceCommand("wake", "") })
			ui.Button(c.Key("voice-cancel"), "取消对话").Disabled(disabled).OnClick(func() { a.voiceCommand("cancel", "") })
			ui.Button(c.Key("voice-clear"), "清空对话").Disabled(disabled).OnClick(func() { a.voiceCommand("clear", "") })
		})
		ui.TextInput(c.Key("voice-preview-text"), &a.voicePreview).Label("试听文本").Placeholder("输入试听文本，留空使用默认问候语").FillWidth()
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c.Key("voice-preview"), "试听语音").Disabled(disabled).OnClick(func() { a.voiceCommand("preview", a.voicePreview) })
			ui.Button(c.Key("voice-reference"), "播放参考音频").Disabled(disabled).OnClick(func() { a.voiceCommand("reference", "") })
		})
		if state.Phase == "error" && state.Response != "" {
			ui.Button(c.Key("voice-retry-response"), "重试回复语音").Disabled(disabled).OnClick(func() { a.voiceCommand("preview", state.Response) })
		}
		ui.Text(c, "操作使用已保存的设置；修改后请先保存。").FontSize(11).TextColor(t.TextMuted).Wrap()
		transcript, response := state.Transcript, state.Response
		if transcript == "" {
			transcript = "等待说话…"
		}
		if response == "" {
			response = "等待回复…"
		}
		ui.Text(c, "你说："+transcript).Wrap().Selectable()
		ui.Text(c, "爱弥斯："+response).Wrap().Selectable()
		ui.Text(c, fmt.Sprintf("第 %d 轮 · 识别 %d ms · 思考 %d ms · 合成累计 %d ms", state.Turn, state.ASRMillis, state.AgentMillis, state.TTSMillis)).FontSize(11).TextColor(t.TextMuted).Wrap()
		if state.GeneratedSegments > 0 || state.Phase == "synthesizing" || state.Phase == "speaking" {
			first := "首段待播放"
			if state.FirstAudioMillis > 0 {
				first = fmt.Sprintf("首段播放 %d ms", state.FirstAudioMillis)
			}
			ui.Text(c, fmt.Sprintf("%s · 已合成 %d 段 · 播放完成 %d 段", first, state.GeneratedSegments, state.PlayedSegments)).FontSize(11).TextColor(t.TextMuted).Wrap()
		}
	})
}

func (a *nativeUI) voiceField(c *ui.Context, key, label, placeholder string, value *string, secret bool) {
	ui.Field(c.Key(key), label, func() {
		field := ui.TextInput(c, value).Label(label).Placeholder(placeholder).FillWidth()
		if secret {
			field = field.Password()
		}
		if field.Changed() {
			a.edited()
		}
	}).FillWidth()
}

func (a *nativeUI) voiceSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	t := c.Theme()
	ui.Column(c.Key("voice-settings")).FillWidth().Padding(20).Gap(10).Radius(18).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "语音设置").Bold().FontSize(17).Grow(1)
			ui.PrimaryButton(c.Key("save-voice-settings"), "保存设置").Disabled(a.busy["save"]).OnClick(a.save)
		})
		if a.dirty {
			ui.Text(c, "未保存").FontSize(12).TextColor(t.Warning)
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "启用语音助手").Grow(1)
			if ui.Switch(c.Key("voice-enabled"), &cfg.Enabled).Label("启用语音助手").Changed() {
				a.edited()
			}
		})
		ui.Text(c, "对话服务 · OpenAI 兼容接口").Bold()
		a.voiceField(c, "voice-llm-url", "OpenAI 服务地址", "https://api.openai.com/v1", &cfg.LLMBaseURL, false)
		a.voiceField(c, "voice-llm-key", "OpenAI API 密钥", "输入 API 密钥", &cfg.LLMAPIKey, true)
		a.voiceField(c, "voice-llm-model", "对话模型", "填写服务提供的模型名称", &cfg.LLMModel, false)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "允许查询监控与控制设备").Grow(1)
			if ui.Switch(c.Key("voice-tools"), &cfg.LLMTools).Label("允许查询监控与控制设备").Changed() {
				a.edited()
			}
		})
		ui.Text(c, "语音服务 · MiMo").Bold()
		a.voiceField(c, "voice-mimo-url", "MiMo 服务地址", "填写 MiMo 服务地址", &cfg.MiMoBaseURL, false)
		a.voiceField(c, "voice-mimo-key", "MiMo API 密钥", "输入 API 密钥", &cfg.MiMoAPIKey, true)
		a.voiceField(c, "voice-reference-path", "参考音频路径", "参考声音的 WAV 文件完整路径", &cfg.ReferenceAudio, false)
		ui.Text(c, "离线唤醒与声音").Bold()
		a.voiceField(c, "voice-wake-dir", "唤醒模型目录", "包含唤醒模型的完整目录路径", &cfg.WakeModelDir, false)
		ui.Text(c, "首次使用：在终端运行 aemeath-voice-assets 安装唤醒模型和参考音频；源码运行时，在 agent 目录执行 go run ./cmd/aemeath-voice-assets。完成后保存设置重试。").FontSize(12).TextColor(t.TextMuted).Wrap().Selectable()
		ui.Text(c, "使用默认安装位置可保留自动填写的路径；自定义位置使用 -dir，并填写对应的 models 目录与 reference.wav 路径。").FontSize(12).TextColor(t.TextMuted).Wrap().Selectable()
		ui.Field(c.Key("voice-wake-threshold"), "唤醒阈值", func() {
			if ui.NumberInput(c, &cfg.WakeThreshold, 0.01, 1, 0.05).FillWidth().Changed() {
				a.edited()
			}
		}).FillWidth().Description("0.01–1，越高越严格。")
		ui.Text(c, fmt.Sprintf("播放音量 · %d%%", cfg.Volume)).FontSize(12).TextColor(t.TextMuted)
		volume := float64(cfg.Volume)
		if ui.Slider(c.Key("voice-volume"), &volume, 0, 100).Label("播放音量").FillWidth().Changed() {
			cfg.Volume = int(volume)
			a.edited()
		}
		ui.Text(c, "角色设定").FontSize(12).TextColor(t.TextMuted)
		if ui.TextArea(c.Key("voice-persona"), &cfg.Persona).Label("角色设定").Placeholder("描述爱弥斯的性格、语气与回答偏好").FillWidth().Height(100).Changed() {
			a.edited()
		}
		ui.PrimaryButton(c.Key("save-voice-settings-bottom"), "保存语音设置").Disabled(a.busy["save"]).OnClick(a.save)
	})
}
