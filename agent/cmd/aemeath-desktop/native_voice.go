package main

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
	"aemeath/agent/internal/wake"

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
		return "语音处理失败"
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
	a.run("voice-command", func() error { return service.VoiceCommand(kind, text) }, func() {
		if kind == "clear" {
			a.voiceHistory = nil
			a.voiceCleared, a.voiceClearTurn = true, a.snapshot.Voice.Turn
			a.voiceClearSession = a.snapshot.Voice.Session
		}
		a.notify("语音指令已发送。")
	})
}

type nativeVoiceTurn struct {
	id                   uint64
	turn                 uint32
	transcript, response string
	state                voice.State
	toolDetails          map[uint64]bool
	restored             bool
}

// Persisted exchanges have no runtime turn/tool metadata. Keep them separate
// from live turns instead of manufacturing timings or playback state.
func (a *nativeUI) restoreVoiceHistory() {
	service, ok := a.service.(interface{ VoiceConversation() []brain.MemoryTurn })
	if !ok {
		return
	}
	turns := service.VoiceConversation()
	for i, turn := range turns {
		// Opening a window during an active session can expose the same final
		// exchange in both memory and the live snapshot. Let recordVoice add
		// that exchange with its actual runtime diagnostics, exactly once.
		s := a.snapshot.Voice
		if i == len(turns)-1 && s.Transcript == turn.User && s.Response == turn.Assistant {
			continue
		}
		a.voiceNextID++
		a.voiceHistory = append(a.voiceHistory, nativeVoiceTurn{
			id: a.voiceNextID, transcript: turn.User, response: turn.Assistant, restored: true,
		})
	}
	if len(a.voiceHistory) > 0 {
		a.voiceScroll.Y = math.MaxFloat32
	}
}

func sameVoiceTurn(turn nativeVoiceTurn, state voice.State) bool {
	return !turn.restored && turn.turn == state.Turn && turn.state.Session == state.Session
}

// Keep a bounded, in-memory transcript of turns observed by this window.
func (a *nativeUI) recordVoice() {
	s := a.snapshot.Voice
	if a.voiceCleared {
		if s.Turn == a.voiceClearTurn && s.Session == a.voiceClearSession {
			return
		}
		a.voiceCleared = false
	}
	if s.Transcript == "" {
		return
	}
	s.ToolCalls = slices.Clone(s.ToolCalls)
	next := nativeVoiceTurn{turn: s.Turn, transcript: s.Transcript, response: s.Response, state: s}
	n := len(a.voiceHistory)
	if n > 0 && reflect.DeepEqual(a.voiceHistory[n-1].state, next.state) {
		return
	}
	follow := a.voiceScroll.Y >= a.voiceScroll.MaxY-8
	if n > 0 && sameVoiceTurn(a.voiceHistory[n-1], s) {
		next.id = a.voiceHistory[n-1].id
		next.toolDetails = a.voiceHistory[n-1].toolDetails
		a.voiceHistory[n-1] = next
	} else {
		// A snapshot may skip a terminal tool event when the next turn starts quickly.
		// Archive it as unknown, rather than claiming it is still running or succeeded.
		if n > 0 {
			for i := range a.voiceHistory[n-1].state.ToolCalls {
				call := &a.voiceHistory[n-1].state.ToolCalls[i]
				if call.Status == "running" {
					call.Status, call.Result = "unknown", "本轮已结束，未收到工具的最终结果。"
				}
			}
		}
		a.voiceNextID++
		next.id = a.voiceNextID
		next.toolDetails = map[uint64]bool{}
		a.voiceHistory = append(a.voiceHistory, next)
		if len(a.voiceHistory) > 100 {
			a.voiceHistory = a.voiceHistory[1:]
		}
	}
	if follow {
		a.voiceScroll.Y = math.MaxFloat32
	}
}

func (a *nativeUI) openVoiceSettings() { a.page, a.settingsTab = "设置", 3 }

func (a *nativeUI) voiceDisabled() bool {
	_, supported := a.service.(nativeVoiceService)
	return !supported || !a.snapshot.Settings.Voice.Enabled || !a.snapshot.Voice.Connected || a.busy["voice-command"]
}

func (a *nativeUI) savedWakePhrase() string {
	phrase := strings.TrimSpace(a.snapshot.Settings.Voice.WakePhrase)
	if phrase == "" {
		return wake.Keyword
	}
	return phrase
}

func (a *nativeUI) voiceView(c *ui.Context) {
	t := c.Theme()
	state := a.snapshot.Voice
	disabled := a.voiceDisabled()
	ui.Column(c.Key("voice-chat")).FillWidth().Grow(1).MinHeight(0).Gap(16).Children(func() {
		ui.Row(c.Key("voice-toolbar")).FillWidth().Shrink(0).Gap(10).Padding(0, 0, 12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border.Alpha(0.5)).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(36, 36).Shrink(0).Radius(12).Background(t.Accent.Alpha(0.1)).Center().Children(func() {
				ui.Icon(c, sidebarVoice).Size(19, 19).TextColor(t.Accent)
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, "爱弥斯").Bold().FontSize(15)
				connection := "语音通道：未连接"
				if state.Connected {
					connection = "语音通道：已连接"
				}
				ui.Text(c, connection+" · "+nativeVoicePhase(state.Phase)).FontSize(11).TextColor(t.TextMuted).Wrap()
			})
			ui.Button(c.Key("voice-configure"), "语音设置").FontSize(12).OnClick(a.openVoiceSettings)
			ui.Button(c.Key("voice-clear"), "清空对话").FontSize(12).Disabled(disabled).OnClick(func() { a.voiceCommand("clear", "") })
		})
		if len(a.voiceHistory) == 0 {
			ui.Column(c.Key("voice-empty")).Label("对话记录").FillWidth().Grow(1).MinHeight(0).Clip().Center().Children(func() {
				ui.Column(c).MaxWidth(400).FillWidth().Padding(16).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(64, 64).Radius(22).Background(t.Accent.Alpha(0.08)).Center().Children(func() {
						ui.Icon(c, sidebarVoice).Size(30, 30).TextColor(t.Accent)
					})
					ui.Text(c, "和爱弥斯聊一聊").FontSize(22).Bold().TextAlign(ui.Center)
					ui.Text(c, fmt.Sprintf("说出「%s」，或点击手动唤醒开始对话。", a.savedWakePhrase())).FontSize(12).TextColor(t.TextMuted).Wrap().TextAlign(ui.Center)
				})
			})
		} else {
			ui.Scroll(c.Key("voice-messages")).Label("对话记录").FillWidth().Grow(1).MinHeight(0).TrackScroll(&a.voiceScroll).Children(func() {
				ui.Column(c).FillWidth().Padding(4, 8, 12).Gap(18).Children(func() {
					for i, turn := range a.voiceHistory {
						ui.Column(c.Key(fmt.Sprintf("voice-turn-%d", turn.id))).FillWidth().Gap(12).Children(func() {
							a.voiceBubble(c, "你", turn.transcript, true)
							if turn.restored {
								ui.Text(c, "历史对话 · 仅恢复问答文字").FontSize(11).TextColor(t.TextMuted)
							} else {
								a.voiceTurnProcessView(c, i)
							}
							if turn.response != "" {
								a.voiceBubble(c, "爱弥斯", turn.response, false)
							} else if sameVoiceTurn(turn, state) && (state.Phase == "thinking" || state.Phase == "recognizing") {
								a.voiceBubble(c, "爱弥斯", "正在思考…", false)
							}
						})
					}
				})
			})
		}
		ui.Column(c.Key("voice-controls")).Label("语音操作栏").FillWidth().Shrink(0).Padding(14, 16).Gap(12).Radius(18).Border(1, t.Border.Alpha(0.5)).Background(t.Surface).Children(func() {
			a.voiceStatusView(c)
			if state.Notice != "" {
				ui.Text(c, state.Notice).FontSize(12).Wrap().MaxLines(2).Ellipsis("…")
			}
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, nativeVoiceActivity(state)).FontSize(12).Bold().Wrap()
					if state.Turn > 0 && !(a.voiceCleared && state.Turn == a.voiceClearTurn && state.Session == a.voiceClearSession) {
						a.voiceLiveProgressView(c)
					} else {
						ui.Text(c, "通过设备麦克风与你对话").FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					}
				})
				ui.Button(c.Key("voice-cancel"), "取消对话").Height(36).FontSize(12).Disabled(disabled).OnClick(func() { a.voiceCommand("cancel", "") })
				ui.PrimaryButton(c.Key("voice-wake"), "手动唤醒").Height(36).Padding(8, 18).FontSize(13).Disabled(disabled).OnClick(func() { a.voiceCommand("wake", "") })
			})
			if state.Phase == "error" && state.Response != "" {
				ui.Button(c.Key("voice-retry-response"), "重试回复语音").Disabled(disabled).OnClick(func() { a.voiceCommand("preview", state.Response) })
			}
		})
	})
}

func (a *nativeUI) voiceBubble(c *ui.Context, speaker, text string, user bool) {
	t := c.Theme()
	align, background, foreground := ui.Start, t.Surface, t.Text
	if user {
		align, background, foreground = ui.End, t.Accent, t.AccentText
	}
	ui.Row(c).FillWidth().Justify(align).Children(func() {
		ui.Column(c).MaxWidth(560).MinWidth(0).Padding(12, 16).Gap(5).Radius(16).Background(background).Children(func() {
			ui.Text(c, speaker).FontSize(11).TextColor(foreground.Alpha(0.7))
			ui.Text(c, text).FontSize(14).TextColor(foreground).Wrap().Selectable()
		})
	})
}

func (a *nativeUI) voiceStatusView(c *ui.Context) {
	t, state := c.Theme(), a.snapshot.Voice
	if _, supported := a.service.(nativeVoiceService); !supported {
		ui.Text(c, "当前服务不支持语音操作，请更新桌面应用。").FontSize(12).TextColor(t.Warning).Wrap()
	}
	wake, color := "离线唤醒：未就绪（模型缺失或不可用）", t.Warning
	if !a.snapshot.Settings.Voice.Enabled {
		wake, color = "语音助手尚未启用，填写设置后保存即可开始。", t.TextMuted
	} else if state.WakeReady {
		wake, color = fmt.Sprintf("离线唤醒：已就绪 · 请说「%s」", a.savedWakePhrase()), t.Success
	}
	ui.Text(c, wake).FontSize(12).TextColor(color).Wrap()
	if state.Error != "" {
		ui.Text(c, "语音提示："+state.Error).FontSize(12).TextColor(t.Warning).Wrap().MaxLines(2).Ellipsis("…").Tooltip(state.Error).Selectable()
	}
}

func (a *nativeUI) voicePreviewView(c *ui.Context) {
	t, state := c.Theme(), a.snapshot.Voice
	disabled := a.voiceDisabled()
	ui.TextInput(c.Key("voice-preview-text"), &a.voicePreview).Label("试听文本").Placeholder("输入试听文本，留空使用默认问候语").FillWidth()
	ui.Row(c).Gap(8).Children(func() {
		ui.Button(c.Key("voice-preview"), "试听语音").Disabled(disabled).OnClick(func() { a.voiceCommand("preview", a.voicePreview) })
		ui.Button(c.Key("voice-reference"), "播放参考音频").Disabled(disabled).OnClick(func() { a.voiceCommand("reference", "") })
	})
	ui.Text(c, fmt.Sprintf("第 %d 轮 · 识别 %d ms · 思考 %d ms · 合成累计 %d ms", state.Turn, state.ASRMillis, state.AgentMillis, state.TTSMillis)).FontSize(11).TextColor(t.TextMuted).Wrap()
	if state.GeneratedSegments > 0 || state.Phase == "synthesizing" || state.Phase == "speaking" {
		first := "首段待播放"
		if state.FirstAudioMillis > 0 {
			first = fmt.Sprintf("首段播放 %d ms", state.FirstAudioMillis)
		}
		ui.Text(c, fmt.Sprintf("%s · 已合成 %d 段 · 播放完成 %d 段", first, state.GeneratedSegments, state.PlayedSegments)).FontSize(11).TextColor(t.TextMuted).Wrap()
	}
}

var (
	voiceKeyShowIcon = sidebarSVG("<path d='M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z'/><circle cx='12' cy='12' r='3'/>")
	voiceKeyHideIcon = sidebarSVG("<path d='M3 3l18 18M10.6 5.1A12 12 0 0 1 12 5c6.5 0 10 7 10 7a19 19 0 0 1-3.1 4M6.1 6.1A23 23 0 0 0 2 12s3.5 7 10 7a12 12 0 0 0 5.9-1.9M9.9 9.9a3 3 0 0 0 4.2 4.2'/>")
)

func (a *nativeUI) voiceField(c *ui.Context, key, label, placeholder string, value *string, secret bool) {
	ui.Field(c.Key(key), label, func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			visible := a.visibleKeys[key]
			field := ui.TextInput(c.Key("input"), value).Label(label).Placeholder(placeholder).Grow(1).MinWidth(0)
			if secret && !visible {
				field = field.Password()
			}
			if field.Changed() {
				a.edited()
			}
			if secret {
				t := c.Theme()
				action, icon, color := "显示", voiceKeyShowIcon, t.TextMuted
				if visible {
					action, icon, color = "隐藏", voiceKeyHideIcon, t.Accent
				}
				button := ui.ButtonBase(c.Key("visibility")).Label(action+" "+label).Tooltip(action+label).Size(30, 28).Shrink(0).Radius(6).Background(ui.Color{})
				if button.Hovered() {
					button.Background(t.SurfaceHover)
				}
				button.Children(func() {
					ui.Icon(c, icon).Size(17, 17).TextColor(color)
				}).OnClick(func() {
					if a.visibleKeys == nil {
						a.visibleKeys = map[string]bool{}
					}
					a.visibleKeys[key] = !a.visibleKeys[key]
				})
			}
		})
	}).FillWidth()
}
