package main

import (
	"fmt"

	"aemeath/agent/internal/wake"

	"github.com/egoist/mygo/ui"
)

type nativeVoiceSettingsGroup struct {
	key, title string
	icon       *ui.SVG
}

var nativeVoiceSettingsGroups = []nativeVoiceSettingsGroup{
	{"general", "基础设置", sidebarSettings},
	{"conversation", "对话服务", sidebarSVG("<path d='M21 11.5a8.5 8.5 0 0 1-8.5 8.5H4l-3 3V11.5A8.5 8.5 0 0 1 9.5 3h3A8.5 8.5 0 0 1 21 11.5Z'/><path d='M6 9h10M6 13h7'/>")},
	{"recognition", "语音识别", sidebarSVG("<path d='M3 10v4M7 6v12M12 3v18M17 6v12M21 10v4'/>")},
	{"synthesis", "语音合成", sidebarSVG("<path d='m11 5-6 4H2v6h3l6 4V5ZM15 8a6 6 0 0 1 0 8M18 5a10 10 0 0 1 0 14'/>")},
	{"wake", "离线唤醒", sidebarVoice},
	{"preview", "声音试听", sidebarSVG("<circle cx='12' cy='12' r='9'/><path d='m10 8 6 4-6 4V8Z'/>")},
}

func (a *nativeUI) voiceSettingsView(c *ui.Context) {
	t := c.Theme()
	if a.voiceSettingsSection < 0 || a.voiceSettingsSection >= len(nativeVoiceSettingsGroups) {
		a.voiceSettingsSection = 0
	}
	ui.Row(c.Key("voice-settings-layout")).Label("语音设置分组").FillWidth().Grow(1).MinHeight(0).Gap(16).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("voice-settings-sidebar")).Label("语音设置导航").Width(128).Shrink(0).Padding(8).Gap(4).Radius(14).Background(t.Surface).Border(1, t.Border.Alpha(0.45)).Children(func() {
			ui.Text(c, "语音配置").FontSize(11).TextColor(t.TextMuted).Padding(6, 8, 10)
			for index, group := range nativeVoiceSettingsGroups {
				selected := a.voiceSettingsSection == index
				button := ui.Button(c.Key(group.key), "").Label(group.title).AccessSelected(selected).FillWidth().Height(36).Padding(7, 8).Gap(8).BorderWidth(0).Radius(8).AlignItems(ui.Center).Justify(ui.Start).Background(ui.Color{})
				color := t.TextMuted
				if selected {
					button.Background(t.Accent.Alpha(0.12))
					color = t.Accent
				} else if button.Hovered() {
					button.Background(t.SurfaceHover)
				}
				button.Children(func() {
					ui.Icon(c, group.icon).Size(16, 16).Shrink(0).TextColor(color)
					ui.Text(c, group.title).FontSize(12).TextColor(color).MinWidth(0).SingleLine()
				}).OnClick(func() { a.voiceSettingsSection = index })
			}
		})
		group := nativeVoiceSettingsGroups[a.voiceSettingsSection]
		ui.Scroll(c.Key("voice-settings-" + group.key)).Label(group.title + "设置内容").Grow(1).MinWidth(0).MinHeight(0).TrackScroll(&a.voiceSettingsScroll[a.voiceSettingsSection]).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Gap(14).Children(func() {
				ui.Text(c, group.title).FontSize(18).Bold()
				ui.Column(c.Key(group.key + "-form")).FillWidth().MinWidth(0).Padding(18).Gap(12).Radius(14).Background(t.Surface).Children(func() {
					switch a.voiceSettingsSection {
					case 1:
						a.voiceConversationSettingsView(c)
					case 2:
						a.voiceRecognitionSettingsView(c)
					case 3:
						a.voiceSynthesisSettingsView(c)
					case 4:
						a.voiceWakeSettingsView(c)
					case 5:
						a.voiceSoundSettingsView(c)
					default:
						a.voiceGeneralSettingsView(c)
					}
				})
			})
		})
	})
}

func (a *nativeUI) voiceGeneralSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "启用语音助手").Grow(1)
		if ui.Switch(c.Key("voice-enabled"), &cfg.Enabled).Label("启用语音助手").Changed() {
			a.edited()
		}
	})
	ui.Field(c.Key("voice-persona"), "角色设定", func() {
		if ui.TextArea(c, &cfg.Persona).Label("角色设定").Placeholder("描述爱弥斯的性格、语气与回答偏好").FillWidth().Height(180).Changed() {
			a.edited()
		}
	}).FillWidth()
}

func (a *nativeUI) voiceConversationSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	ui.Text(c, "OpenAI 兼容接口").Bold()
	a.voiceField(c, "voice-llm-url", "OpenAI 服务地址", "https://api.openai.com/v1", &cfg.LLMBaseURL, false)
	a.voiceField(c, "voice-llm-key", "OpenAI API 密钥", "输入 API 密钥", &cfg.LLMAPIKey, true)
	a.voiceField(c, "voice-llm-model", "对话模型", "填写服务提供的模型名称", &cfg.LLMModel, false)
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "启用 Agent 工具").Grow(1).MinWidth(0).Wrap()
		if ui.Switch(c.Key("voice-tools"), &cfg.LLMTools).Label("启用 Agent 工具").Changed() {
			a.edited()
		}
	})
	ui.Text(c, "支持时间查询、网页搜索、查询系统与额度，以及控制设备和电脑锁屏。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
	a.voiceField(c, "voice-tavily-key", "Tavily API 密钥", "tvly-…", &cfg.TavilyAPIKey, true)
}

func (a *nativeUI) voiceRecognitionSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	ui.Text(c, "MiMo").Bold()
	a.voiceField(c, "voice-mimo-url", "MiMo 服务地址", "填写 MiMo 服务地址", &cfg.MiMoBaseURL, false)
	a.voiceField(c, "voice-mimo-key", "MiMo API 密钥", "输入 API 密钥", &cfg.MiMoAPIKey, true)
}

func (a *nativeUI) voiceSynthesisSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	t := c.Theme()
	ui.Text(c, "语音合成提供方").Bold()
	provider := 0
	if cfg.TTSProvider == "fish" {
		provider = 1
	}
	if ui.Segmented(c.Key("voice-tts-provider"), &provider, "MiMo", "Fish").Label("语音合成提供方").Changed() {
		cfg.TTSProvider = []string{"mimo", "fish"}[provider]
		a.edited()
	}
	if cfg.TTSProvider == "fish" {
		a.voiceField(c, "voice-fish-url", "Fish 服务地址", "https://api.fish.audio", &cfg.FishBaseURL, false)
		a.voiceField(c, "voice-fish-key", "Fish API 密钥", "输入 Fish API 密钥", &cfg.FishAPIKey, true)
		a.voiceField(c, "voice-fish-model", "Fish 模型", "s2.1-pro", &cfg.FishModel, false)
		a.voiceField(c, "voice-fish-reference-id", "Fish reference_id", "优先填写已创建的音色 ID", &cfg.FishReferenceID, false)
		ui.Text(c, "填写音色 ID 后，无需参考音频或转写文本；留空时请提供 WAV 和对应的逐字转写。").FontSize(11).TextColor(t.TextMuted).Wrap()
		ui.Field(c.Key("voice-fish-reference-text"), "Fish 参考音频转写", func() {
			if ui.TextArea(c, &cfg.FishReferenceText).Label("Fish 参考音频转写").Placeholder("与参考音频逐字一致；使用 reference_id 时可留空").FillWidth().Height(90).Changed() {
				a.edited()
			}
		}).FillWidth()
		latency := 0
		if cfg.FishLatency == "normal" {
			latency = 1
		} else if cfg.FishLatency == "low" {
			latency = 2
		}
		ui.Text(c, "Fish 延迟偏好").FontSize(12).TextColor(t.TextMuted)
		if ui.Segmented(c.Key("voice-fish-latency"), &latency, "均衡", "标准", "低延迟").Label("Fish 延迟偏好").Changed() {
			cfg.FishLatency = []string{"balanced", "normal", "low"}[latency]
			a.edited()
		}
	} else {
		ui.Text(c, "MiMo 合成与语音识别共用服务地址和密钥。").FontSize(11).TextColor(t.TextMuted).Wrap()
		ui.Button(c.Key("voice-configure-mimo"), "配置 MiMo 服务").OnClick(func() { a.voiceSettingsSection = 2 })
	}
	a.voiceField(c, "voice-reference-path", "参考音频路径", "参考声音的 WAV 文件完整路径", &cfg.ReferenceAudio, false)
}

func (a *nativeUI) voiceWakeSettingsView(c *ui.Context) {
	a.voiceField(c, "voice-wake-phrase", "唤醒词", wake.Keyword, &a.draft.Voice.WakePhrase, false)
	ui.Text(c, "留空使用默认唤醒词「你好小爱」。支持 2–12 个模型支持的中文汉字，自动生成拼音；多音字采用默认读音。保存后会重新连接并应用。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
	a.wakeModelsView(c)
	ui.Field(c.Key("voice-wake-threshold"), "唤醒阈值", func() {
		if ui.NumberInput(c, &a.draft.Voice.WakeThreshold, 0.01, 1, 0.05).FillWidth().Changed() {
			a.edited()
		}
	}).FillWidth().Description("0.01–1，越高越严格。")
}

func (a *nativeUI) voiceSoundSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	ui.Text(c, fmt.Sprintf("播放音量 · %d%%", cfg.Volume)).FontSize(12).TextColor(c.Theme().TextMuted)
	volume := float64(cfg.Volume)
	if ui.Slider(c.Key("voice-volume"), &volume, 0, 100).Label("播放音量").FillWidth().Changed() {
		cfg.Volume = int(volume)
		a.edited()
	}
	a.voicePreviewView(c)
}
