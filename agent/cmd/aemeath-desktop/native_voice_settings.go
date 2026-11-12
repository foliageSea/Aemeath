package main

import (
	"fmt"

	"aemeath/agent/internal/wake"

	"github.com/egoist/mygo/ui"
)

type nativeSettingsGroup struct {
	key, title string
	icon       *ui.SVG
}

var nativeAssistantSettingsGroups = []nativeSettingsGroup{
	{"general", "基础设置", voiceSettingsIcon},
	{"conversation", "对话服务", sidebarSVG("<path d='M21 11.5a8.5 8.5 0 0 1-8.5 8.5H4l-3 3V11.5A8.5 8.5 0 0 1 9.5 3h3A8.5 8.5 0 0 1 21 11.5Z'/><path d='M6 9h10M6 13h7'/>")},
}

var nativeVoiceSettingsGroups = []nativeSettingsGroup{
	{"recognition", "语音识别", sidebarSVG("<path d='M3 10v4M7 6v12M12 3v18M17 6v12M21 10v4'/>")},
	{"synthesis", "语音合成", sidebarSVG("<path d='m11 5-6 4H2v6h3l6 4V5ZM15 8a6 6 0 0 1 0 8M18 5a10 10 0 0 1 0 14'/>")},
	{"wake", "离线唤醒", sidebarVoice},
	{"preview", "声音试听", sidebarSVG("<circle cx='12' cy='12' r='9'/><path d='m10 8 6 4-6 4V8Z'/>")},
}

func (a *nativeUI) assistantSettingsView(c *ui.Context) {
	a.groupedSettingsView(c, "assistant", "助手", nativeAssistantSettingsGroups, &a.assistantSettingsSection, a.assistantSettingsScroll[:])
}

func (a *nativeUI) voiceSettingsView(c *ui.Context) {
	a.groupedSettingsView(c, "voice", "语音", nativeVoiceSettingsGroups, &a.voiceSettingsSection, a.voiceSettingsScroll[:])
}

func (a *nativeUI) groupedSettingsView(c *ui.Context, key, title string, groups []nativeSettingsGroup, section *int, scroll []ui.ScrollState) {
	t := c.Theme()
	if *section < 0 || *section >= len(groups) {
		*section = 0
	}
	ui.Row(c.Key(key + "-settings-layout")).Label(title + "设置分组").FillWidth().MaxWidth(nativeSettingsPageWidth).Grow(1).MinHeight(0).Gap(nativeSettingsNavigationGap).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key(key+"-settings-sidebar")).Label(title+"设置导航").Width(nativeSettingsNavigationWidth).Shrink(0).Padding(8).Gap(4).Radius(14).Background(t.Surface).Border(1, t.Border.Alpha(0.45)).Children(func() {
			for index, group := range groups {
				selected := *section == index
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
				}).OnClick(func() { *section = index })
			}
		})
		group := groups[*section]
		ui.Scroll(c.Key(key + "-settings-" + group.key)).Label(group.title + "设置内容").Grow(1).MinWidth(0).MinHeight(0).TrackScroll(&scroll[*section]).Children(func() {
			nativeSettingsBody(c, group.key+"-form", func() {
				ui.Text(c, group.title).FontSize(18).Bold()
				ui.Column(c).FillWidth().MinWidth(0).Gap(24).Children(func() {
					switch group.key {
					case "general":
						a.voiceGeneralSettingsView(c)
					case "conversation":
						a.voiceConversationSettingsView(c)
					case "recognition":
						a.voiceRecognitionSettingsView(c)
					case "synthesis":
						a.voiceSynthesisSettingsView(c)
					case "wake":
						a.voiceWakeSettingsView(c)
					case "preview":
						a.voiceSoundSettingsView(c)
					}
				})
			})
		})
	})
}

func (a *nativeUI) voiceGeneralSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	nativeSettingsSection(c, "voice-general-card", "", func() {
		if nativeSettingsToggle(c, "voice-enabled", "启用语音助手", &cfg.Enabled, false) {
			a.edited()
		}
		nativeSettingsTextArea(c, "voice-persona", "角色设定", func() ui.Element {
			field := ui.TextArea(c, &cfg.Persona).Label("角色设定").Placeholder("描述爱弥斯的性格、语气与回答偏好").FillWidth().MinWidth(0).Height(180)
			if field.Changed() {
				a.edited()
			}
			return field
		})
	})
}

func (a *nativeUI) voiceConversationSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	nativeSettingsSection(c, "voice-service-card", "服务连接", func() {
		a.voiceField(c, "voice-llm-url", "OpenAI 服务地址", "https://api.openai.com/v1", &cfg.LLMBaseURL, false)
		a.voiceField(c, "voice-llm-key", "OpenAI API 密钥", "输入 API 密钥", &cfg.LLMAPIKey, true)
		a.voiceField(c, "voice-llm-model", "对话模型", "填写服务提供的模型名称", &cfg.LLMModel, false)
	})
	nativeSettingsSection(c, "voice-context-card", "上下文", func() {
		autoContextWindow := cfg.ContextWindowMode != "manual"
		if nativeSettingsToggle(c, "voice-context-auto", "自动读取模型上下文容量", &autoContextWindow, false) {
			cfg.ContextWindowMode = "manual"
			if autoContextWindow {
				cfg.ContextWindowMode = "auto"
			}
			a.edited()
		}
		if autoContextWindow {
			ui.Column(c).FillWidth().MinWidth(0).Gap(16).Tooltip("从 models.dev 公共目录读取容量，未匹配时使用备用值，不向对话服务查询容量。后台下载不发送密钥或对话；目录容量不保证等于当前服务实际上限。").Children(func() {
				if nativeSettingsToggle(c, "voice-models-dev", "使用 models.dev 查询上下文容量", &cfg.ModelsDevEnabled, false) {
					a.edited()
				}
				if cfg.ModelsDevEnabled {
					a.voiceField(c, "voice-models-dev-provider", "models.dev 服务商 ID", "留空按服务地址精确匹配，如 openai", &cfg.ModelsDevProvider, false)
					a.voiceField(c, "voice-models-dev-model", "models.dev 模型 ID", "留空使用请求模型 ID；别名映射需填写服务商 ID", &cfg.ModelsDevModel, false)
				}
			})
		}
		capacityLabel := "备用上下文容量（Tokens）"
		capacityDescription := "自动读取失败时使用；0 为默认 32768（32K），范围 8192–2097152。保存后重连生效。"
		if !autoContextWindow {
			capacityLabel = "手动上下文容量（Tokens）"
			capacityDescription = "手动设置模型上限；0 为默认 32768（32K），范围 8192–2097152。保存后重连生效。"
		}
		nativeSettingsNumberField(c, "voice-context-window", capacityLabel, func() ui.Element {
			capacity := float64(cfg.ContextWindow)
			if capacity == 0 {
				capacity = 32768
			}
			field := ui.NumberInput(c, &capacity, 0, 2097152, 1024).Label(capacityLabel).FillWidth().MinWidth(0).Height(36).Tooltip(capacityDescription)
			if field.Changed() {
				cfg.ContextWindow = int(capacity)
				a.edited()
			}
			return field
		}, capacityDescription)
	})
	nativeSettingsSection(c, "voice-tools-card", "工具与推理", func() {
		if nativeSettingsToggle(c, "voice-tools", "启用 Agent 工具", &cfg.LLMTools, false) {
			a.edited()
		}
		ui.Text(c, "支持时间查询、网页搜索、查询系统与额度，以及控制设备和电脑锁屏。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
		nativeSettingsField(c, "voice-reasoning-field", "思考强度", func() ui.Element {
			efforts := []string{"", "low", "medium", "high"}
			effort := 0
			for i, value := range efforts {
				if cfg.ReasoningEffort == value {
					effort = i
				}
			}
			field := nativeSettingsSegmented(c, "voice-reasoning-effort", &effort, "思考强度", "默认", "低", "中", "高")
			if field.Changed() {
				cfg.ReasoningEffort = efforts[effort]
				a.edited()
			}
			return field
		})
		a.voiceField(c, "voice-tavily-key", "Tavily API 密钥", "tvly-…", &cfg.TavilyAPIKey, true)
	})
}

func (a *nativeUI) voiceRecognitionSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	nativeSettingsSection(c, "voice-recognition-card", "MiMo", func() {
		a.voiceField(c, "voice-mimo-url", "MiMo 服务地址", "填写 MiMo 服务地址", &cfg.MiMoBaseURL, false)
		a.voiceField(c, "voice-mimo-key", "MiMo API 密钥", "输入 API 密钥", &cfg.MiMoAPIKey, true)
	})
}

func (a *nativeUI) voiceSynthesisSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	nativeSettingsSection(c, "voice-synthesis-card", "", func() {
		nativeSettingsField(c, "voice-provider-field", "语音合成提供方", func() ui.Element {
			provider := 0
			if cfg.TTSProvider == "fish" {
				provider = 1
			}
			field := nativeSettingsSegmented(c, "voice-tts-provider", &provider, "语音合成提供方", "MiMo", "Fish")
			if field.Changed() {
				cfg.TTSProvider = []string{"mimo", "fish"}[provider]
				a.edited()
			}
			return field
		})
		if cfg.TTSProvider == "fish" {
			a.voiceField(c, "voice-fish-url", "Fish 服务地址", "https://api.fish.audio", &cfg.FishBaseURL, false)
			a.voiceField(c, "voice-fish-key", "Fish API 密钥", "输入 Fish API 密钥", &cfg.FishAPIKey, true)
			a.voiceField(c, "voice-fish-model", "Fish 模型", "s2.1-pro", &cfg.FishModel, false)
			a.voiceField(c, "voice-fish-reference-id", "Fish reference_id", "优先填写已创建的音色 ID", &cfg.FishReferenceID, false)
			ui.Text(c, "填写音色 ID 后，无需参考音频或转写文本；留空时请提供 WAV 和对应的逐字转写。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
			nativeSettingsTextArea(c, "voice-fish-reference-text", "Fish 参考音频转写", func() ui.Element {
				field := ui.TextArea(c, &cfg.FishReferenceText).Label("Fish 参考音频转写").Placeholder("与参考音频逐字一致；使用 reference_id 时可留空").FillWidth().MinWidth(0).Height(90)
				if field.Changed() {
					a.edited()
				}
				return field
			})
			nativeSettingsField(c, "voice-fish-latency-field", "Fish 延迟偏好", func() ui.Element {
				latency := 0
				if cfg.FishLatency == "normal" {
					latency = 1
				} else if cfg.FishLatency == "low" {
					latency = 2
				}
				field := nativeSettingsSegmented(c, "voice-fish-latency", &latency, "Fish 延迟偏好", "均衡", "标准", "低延迟")
				if field.Changed() {
					cfg.FishLatency = []string{"balanced", "normal", "low"}[latency]
					a.edited()
				}
				return field
			})
		} else {
			ui.Text(c, "MiMo 合成与语音识别共用服务地址和密钥。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
		}
		a.voiceField(c, "voice-reference-path", "参考音频路径", "参考声音的 WAV 文件完整路径", &cfg.ReferenceAudio, false)
		if cfg.TTSProvider != "fish" {
			nativeSettingsActions(c, "voice-mimo-actions", func() {
				ui.Button(c.Key("voice-configure-mimo"), "配置 MiMo 服务").OnClick(func() { a.voiceSettingsSection = 0 })
			})
		}
	})
}

func (a *nativeUI) voiceWakeSettingsView(c *ui.Context) {
	nativeSettingsSection(c, "voice-wake-card", "", func() {
		a.voiceField(c, "voice-wake-phrase", "唤醒词", wake.Keyword, &a.draft.Voice.WakePhrase, false)
		ui.Text(c, "留空使用默认唤醒词「你好小爱」。支持 2–12 个模型支持的中文汉字，自动生成拼音；多音字采用默认读音。保存后会重新连接并应用。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
	})
	a.wakeModelsView(c)
	nativeSettingsSection(c, "voice-wake-threshold-card", "", func() {
		nativeSettingsNumberField(c, "voice-wake-threshold", "唤醒阈值", func() ui.Element {
			field := ui.NumberInput(c, &a.draft.Voice.WakeThreshold, 0.01, 1, 0.05).Label("唤醒阈值").FillWidth().MinWidth(0).Height(36).Tooltip("0.01–1，越高越严格。")
			if field.Changed() {
				a.edited()
			}
			return field
		}, "0.01–1，越高越严格。")
	})
}

func (a *nativeUI) voiceSoundSettingsView(c *ui.Context) {
	cfg := &a.draft.Voice
	nativeSettingsSection(c, "voice-sound-card", "", func() {
		nativeSettingsField(c, "voice-playback-output-field", "播放位置", func() ui.Element {
			// Preserve legacy config routes: "console" plays on the computer
			// running Agent; "agent" (and the default) plays on the ESP32 monitor.
			output := 1
			if cfg.PlaybackOutput == "console" {
				output = 0
			}
			field := nativeSettingsSegmented(c, "voice-playback-output", &output, "播放位置", "Agent", "监控台")
			if field.Changed() {
				cfg.PlaybackOutput = []string{"console", "agent"}[output]
				a.edited()
			}
			return field
		})
		ui.Text(c, "Agent 使用当前电脑的扬声器；监控台使用 ESP32 设备扬声器。保存后生效。").FontSize(11).TextColor(c.Theme().TextMuted).Wrap()
		nativeSettingsField(c, "voice-volume-field", "播放音量", func() ui.Element {
			volume := float64(cfg.Volume)
			field := ui.Slider(c.Key("voice-volume"), &volume, 0, 100).Label("播放音量").Tooltip(fmt.Sprintf("%d%%", cfg.Volume)).FillWidth()
			if field.Changed() {
				cfg.Volume = int(volume)
				a.edited()
			}
			return field
		})
		a.voicePreviewView(c)
	})
}
