package main

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/voice"
	"aemeath/agent/internal/wake"

	"github.com/egoist/mygo/plugins/glass"
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
	if a.voiceHold {
		a.endVoiceHold(true)
	}
	service, ok := a.service.(nativeVoiceService)
	if !ok {
		a.notify("当前服务不支持语音操作，请更新桌面应用。")
		return
	}
	textRevision := a.voiceTextRevision
	a.run("voice-command", func() error { return service.VoiceCommand(kind, text) }, func() {
		if kind == "text" {
			if a.voiceTextRevision == textRevision {
				a.voiceText = ""
			}
			a.voiceScroll.Y = math.MaxFloat32
			a.notify("文本消息已发送。")
			return
		}
		if kind == "clear" {
			a.voiceHistory = nil
			a.voiceCleared, a.voiceClearTurn = true, a.snapshot.Voice.Turn
			a.voiceClearSession = a.snapshot.Voice.Session
		}
		a.notify("语音指令已发送。")
	})
}

type nativeVoiceTurn struct {
	id                       uint64
	turn                     uint32
	transcript, response     string
	transcriptAt, responseAt time.Time
	state                    voice.State
	toolDetails              map[uint64]bool
	restored                 bool
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
	// Capture first arrival, not every status refresh or streamed text update.
	now := time.Now()
	next := nativeVoiceTurn{turn: s.Turn, transcript: s.Transcript, response: s.Response, transcriptAt: now, state: s}
	if s.Response != "" {
		next.responseAt = now
	}
	n := len(a.voiceHistory)
	if n > 0 && reflect.DeepEqual(a.voiceHistory[n-1].state, next.state) {
		return
	}
	follow := a.voiceScroll.Y >= a.voiceScroll.MaxY-8
	if n > 0 && sameVoiceTurn(a.voiceHistory[n-1], s) {
		previous := a.voiceHistory[n-1]
		next.id = previous.id
		next.toolDetails = previous.toolDetails
		next.transcriptAt = previous.transcriptAt
		if !previous.responseAt.IsZero() {
			next.responseAt = previous.responseAt
		}
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

func (a *nativeUI) openVoiceSettings() { a.page, a.settingsTab = "设置", nativeSettingsVoiceTab }

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

func (a *nativeUI) voicePathLabel() string {
	if a.snapshot.Settings.RunMode == desktop.RunModeStandalone {
		if a.snapshot.Voice.Phase == "speaking" {
			return "语音路径：本机麦克风 → 本机扬声器 · 播放时暂停自动唤醒，可手动打断"
		}
		return "语音路径：本机麦克风 → 本机扬声器"
	}
	if a.snapshot.Settings.Voice.PlaybackOutput == "console" {
		return "语音路径：ESP32 麦克风 → 本机扬声器"
	}
	return "语音路径：ESP32 麦克风 → ESP32 扬声器"
}

func (a *nativeUI) voiceView(c *ui.Context) {
	t := c.Theme()
	state := a.snapshot.Voice
	disabled := a.voiceDisabled()
	ui.Column(c.Key("voice-chat")).FillWidth().Grow(1).MinHeight(0).Gap(16).Children(func() {
		ui.Row(c.Key("voice-toolbar")).Label("语音助手状态栏").FillWidth().Shrink(0).Gap(10).Padding(0, 0, 12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border.Alpha(0.5)).AlignItems(ui.Center).Children(func() {
			voiceAvatar(c, 40)
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Row(c.Key("voice-connection")).Label("助手连接状态").FillWidth().MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "爱弥斯").Bold().FontSize(15).Shrink(0)
					connection, detail := "未连接", "语音通道：未连接"
					if state.Connected {
						connection = "已连接 · " + nativeVoicePhase(state.Phase)
						detail = "语音通道：" + connection
					}
					ui.Text(c, "· "+connection).MinWidth(0).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(detail)
					ui.Text(c.Key("voice-path"), a.voicePathLabel()).Label(a.voicePathLabel()).Grow(1).MinWidth(0).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(a.voicePathLabel())
				})
				ui.Row(c.Key("voice-header-hints")).Label("助手唤醒与快捷键").FillWidth().MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
					a.voiceStatusView(c)
					ui.Text(c, "·").FontSize(11).TextColor(t.TextMuted).Shrink(0)
					a.voiceHoldHintView(c)
				})
			})
			voiceIconButton(c, "voice-configure", "语音设置", voiceSettingsIcon, false, false, a.openVoiceSettings)
			voiceIconButton(c, "voice-clear", "清空对话", voiceClearIcon, false, disabled, func() { a.voiceClearConfirm = true })
		})
		if ui.AlertDialog(c, &a.voiceClearConfirm, "清空对话？", "当前对话记录、上下文和累计用量将被清除，且无法恢复。", "取消", "清空") == 1 {
			a.voiceCommand("clear", "")
		}
		ui.Box(c.Key("voice-conversation")).FillWidth().Grow(1).MinHeight(0).Clip().Children(func() {
			composerInset := max(156, a.voiceControlsHeight) + 16
			if len(a.voiceHistory) == 0 {
				ui.Column(c.Key("voice-empty")).Label("对话记录").Fill().MinHeight(0).Padding(0, 0, composerInset).Clip().Center().Children(func() {
					ui.Column(c).MaxWidth(400).FillWidth().Padding(16).Gap(12).AlignItems(ui.Center).Children(func() {
						voiceAvatar(c, 88)
						ui.Text(c, "和爱弥斯聊一聊").FontSize(22).Bold().TextAlign(ui.Center)
						ui.Text(c, fmt.Sprintf("说出「%s」，或点击手动唤醒开始对话。", a.savedWakePhrase())).FontSize(12).TextColor(t.TextMuted).Wrap().TextAlign(ui.Center)
						ui.Text(c, "也可以在下方输入文字开始对话。").FontSize(12).TextColor(t.TextMuted).Wrap().TextAlign(ui.Center)
					})
				})
			} else {
				ui.Scroll(c.Key("voice-messages")).Label("对话记录").Fill().MinHeight(0).ScrollbarInsets(0, 0, composerInset, 0).TrackScroll(&a.voiceScroll).Children(func() {
					ui.Column(c).FillWidth().Padding(12, 12, composerInset+24).AlignItems(ui.Center).Children(func() {
						ui.Column(c.Key("voice-reading-column")).FillWidth().MaxWidth(860).MinWidth(0).Gap(32).Children(func() {
							for i, turn := range a.voiceHistory {
								ui.Column(c.Key(fmt.Sprintf("voice-turn-%d", turn.id))).FillWidth().MinWidth(0).Gap(18).Children(func() {
									a.voiceBubble(c, "你", turn.transcript, true, nativeVoiceMessageTime(turn.transcriptAt))
									if !turn.restored {
										ui.Column(c).FillWidth().MaxWidth(688).MinWidth(0).Padding(0, 0, 0, 48).Children(func() {
											a.voiceTurnProcessView(c, i)
										})
									}
									if turn.response != "" {
										a.voiceBubble(c, "爱弥斯", turn.response, false, nativeVoiceMessageTime(turn.responseAt))
									} else if sameVoiceTurn(turn, state) && (state.Phase == "thinking" || state.Phase == "recognizing") {
										a.voiceBubble(c, "爱弥斯", "正在思考…", false, "")
									}
								})
							}
						})
					})
				})
			}
			a.voiceControlsView(c)
		})
	})
}

func (a *nativeUI) sendVoiceText() {
	text := strings.TrimSpace(a.voiceText)
	if a.voiceDisabled() || text == "" {
		return
	}
	if len(text) > 4096 {
		a.notify("文本消息过长，请缩短后重试（最多 4096 字节）。")
		return
	}
	a.voiceCommand("text", text)
}

func (a *nativeUI) voiceControlsView(c *ui.Context) {
	t := c.Theme()
	// Blur only the backdrop; the composer contents stay sharp.
	card := ui.Column(c.Key("voice-controls")).Label("语音操作栏").Absolute().Left(0).Right(0).Bottom(0).MinWidth(0).Radius(16).Border(1, t.Border.Alpha(0.55)).Material(glass.Blur{Radius: 20})
	if height := card.Bounds().H; height > 0 && height != a.voiceControlsHeight {
		a.voiceControlsHeight = height
		c.Invalidate()
	}
	if card.FocusWithin() {
		card.BorderColor(t.Accent.Alpha(0.55))
	}
	card.Children(func() {
		ui.Column(c.Key("voice-controls-tint")).FillWidth().MinWidth(0).Radius(16).Background(t.Surface.Alpha(0.72)).Children(func() {
			a.voiceTextInputView(c)
			a.voiceContextView(c, true)
			_, supported := a.service.(nativeVoiceService)
			if !supported || a.snapshot.Voice.Error != "" || a.voiceGlobalError != "" || a.snapshot.Voice.Notice != "" {
				ui.Column(c.Key("voice-notices")).FillWidth().MinWidth(0).Padding(0, 16, 12).Gap(4).Children(func() { a.voiceNoticeView(c) })
			}
		})
	})
}

func nativeVoiceCanCancel(phase string) bool {
	switch phase {
	case "wake", "listening", "recognizing", "thinking", "synthesizing", "speaking":
		return true
	default:
		return false
	}
}

func (a *nativeUI) voiceComposerStatus() string {
	state := a.snapshot.Voice
	if !a.snapshot.Settings.Voice.Enabled {
		return "助手未启用"
	}
	if !state.Connected {
		if state.Phase == "connecting" {
			return "正在连接…"
		}
		return "连接已断开"
	}
	if a.voiceHold {
		return "正在录音 · 松开快捷键发送"
	}
	if state.Phase == "speaking" && state.GeneratedSegments > 0 {
		return fmt.Sprintf("正在播放回复 · 已播 %d/%d 段", state.PlayedSegments, state.GeneratedSegments)
	}
	return nativeVoiceActivity(state)
}

func (a *nativeUI) voiceTextInputView(c *ui.Context) {
	state := a.snapshot.Voice
	width, _ := c.Size()
	ui.Column(c.Key("voice-text-composer")).FillWidth().MinWidth(0).Padding(14, 16, 10).Gap(8).Children(func() {
		input := ui.TextArea(c.Key("voice-text-input"), &a.voiceText).Label("文本消息").Placeholder("输入消息…").FillWidth().MinWidth(0).Padding(0).BorderWidth(0).Background(ui.Color{}).Radius(0).FontSize(14).FixedLineHeight(20)
		inputWidth := input.Bounds().W
		if inputWidth <= 0 {
			inputWidth = max(160, width-280)
		}
		// Include a final glyph so a trailing newline gets its own editable line.
		_, lineHeight := c.MeasureText(inputWidth, ui.Span{Text: "M", Size: 14})
		_, textHeight := c.MeasureText(inputWidth, ui.Span{Text: a.voiceText + " ", Size: 14})
		lines := float32(math.Ceil(float64(textHeight / max(1, lineHeight))))
		input.Height(max(2, min(6, lines)) * 20).MinHeight(40)
		input.OnTextKey(func(k ui.TextKey) bool {
			if k.Key != ui.KeyEnter || k.Mods != 0 {
				return false
			}
			a.voiceTextSubmit = true
			return true
		})
		if input.Changed() {
			a.voiceTextRevision++
		}
		ui.Row(c.Key("voice-composer-actions")).FillWidth().MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).Children(func() { a.voiceContextView(c, false) })
			if state.Phase == "error" && state.Response != "" {
				voiceIconButton(c, "voice-retry-response", "重试回复语音", voiceRetryIcon, false, a.voiceDisabled(), func() { a.voiceCommand("preview", state.Response) })
			}
			if nativeVoiceCanCancel(state.Phase) || a.voiceHold {
				voiceIconButton(c, "voice-cancel", "取消对话", voiceCancelIcon, false, a.voiceDisabled(), func() { a.voiceCommand("cancel", "") })
			}
			voiceIconButton(c, "voice-wake", "手动唤醒", sidebarVoice, false, a.voiceDisabled(), func() { a.voiceCommand("wake", "") })
			ui.PrimaryButton(c.Key("voice-text-send"), "↑ 发送").Label("发送文本消息").Height(36).Padding(0, 14).Radius(18).Shrink(0).Disabled(a.voiceDisabled() || strings.TrimSpace(a.voiceText) == "").OnClick(a.sendVoiceText)
		})
		if a.voiceTextSubmit {
			a.voiceTextSubmit = false
			a.sendVoiceText()
		}
	})
}

func nativeVoiceMessageTime(sentAt time.Time) string {
	if sentAt.IsZero() {
		return "此消息未记录时间"
	}
	return sentAt.Local().Format("2006年01月02日 15:04:05")
}

func (a *nativeUI) voiceBubble(c *ui.Context, speaker, text string, user bool, timestamp string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	t := c.Theme()
	align, background, foreground := ui.Start, t.Surface, t.Text
	border := t.Border.Alpha(0.5)
	radii := []float32{6, 18, 18, 18}
	if user {
		align, background, foreground = ui.End, t.Accent, t.AccentText
		border, radii = t.Accent.Alpha(0.3), []float32{18, 6, 18, 18}
	}
	ui.Row(c).FillWidth().MinWidth(0).Gap(12).AlignItems(ui.Start).Justify(align).Children(func() {
		if !user {
			voiceAvatar(c, 36)
		}
		ui.Column(c).MaxWidth(640).MinWidth(0).Shrink(1).Gap(7).AlignItems(align).Children(func() {
			ui.Text(c, speaker).FontSize(11).TextColor(t.TextMuted)
			bubble := ui.Column(c).MinWidth(0).Padding(14, 18).Radius(radii...).Background(background).Border(1, border)
			if timestamp != "" {
				bubble.Tooltip(timestamp)
			}
			bubble.Children(func() {
				ui.Text(c, text).FontSize(15).LineHeight(1.65).TextColor(foreground).Wrap().Selectable()
			})
		})
	})
}

var (
	voiceCancelIcon   = sidebarSVG("<path d='M6 6l12 12M18 6 6 18'/>")
	voiceSettingsIcon = sidebarSVG("<path d='M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1'/><circle cx='15' cy='6' r='2'/><circle cx='9' cy='12' r='2'/><circle cx='17' cy='18' r='2'/>")
	voiceClearIcon    = sidebarSVG("<path d='M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3'/>")
	voiceRetryIcon    = sidebarSVG("<path d='M20 12a8 8 0 1 1-2.34-5.66M20 4v5h-5'/>")
)

func voiceIconButton(c *ui.Context, key, label string, icon *ui.SVG, primary, disabled bool, onClick func()) {
	t := c.Theme()
	background, hover, border, color := t.Surface, t.SurfaceHover, t.Border, t.TextMuted
	if primary {
		background, hover, border, color = t.Accent, t.Accent.Alpha(0.85), t.Accent, t.AccentText
	}
	if disabled {
		background, hover, border, color = background.Alpha(0.5), background.Alpha(0.5), border.Alpha(0.5), color.Alpha(0.5)
	}
	size := float32(36)
	if primary {
		size = 40
	}
	button := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(label).Size(size, size).Shrink(0).Radius(size/2).Border(1, border).Background(background).Center().Disabled(disabled)
	if !disabled && button.Hovered() {
		button.Background(hover)
	}
	button.Children(func() {
		ui.Icon(c, icon).FontSize(18).TextColor(color)
	}).OnClick(onClick)
}

func nativeVoicePhaseColor(t *ui.Theme, state voice.State) ui.Color {
	switch state.Phase {
	case "error":
		return t.Warning
	case "wake", "listening", "recognizing", "thinking", "synthesizing", "speaking", "connecting":
		return t.Accent
	default:
		return t.TextMuted
	}
}

func (a *nativeUI) voiceNoticeView(c *ui.Context) {
	t, state := c.Theme(), a.snapshot.Voice
	if _, supported := a.service.(nativeVoiceService); !supported {
		ui.Text(c, "当前服务不支持语音操作，请更新桌面应用。").FontSize(12).TextColor(t.Warning).Wrap()
	}
	if state.Error != "" {
		ui.Text(c, "语音提示："+state.Error).FontSize(12).TextColor(t.Warning).Wrap().MaxLines(2).Ellipsis("…").Tooltip(state.Error).Selectable()
	}
	if a.voiceGlobalError != "" {
		ui.Text(c, a.voiceGlobalError).FontSize(12).TextColor(t.Warning).Wrap()
	}
	if state.Notice != "" {
		ui.Text(c, state.Notice).FontSize(12).Wrap().MaxLines(2).Ellipsis("…")
	}
}

func (a *nativeUI) voiceStatusView(c *ui.Context) {
	t, state := c.Theme(), a.snapshot.Voice
	wake, detail, color := "唤醒未就绪", "离线唤醒：未就绪（模型缺失或不可用）", t.Warning
	if !a.snapshot.Settings.Voice.Enabled {
		wake, detail, color = "助手未启用", "语音助手尚未启用，填写设置后保存即可开始。", t.TextMuted
	} else if state.WakeReady {
		wake, detail, color = "唤醒已就绪", fmt.Sprintf("离线唤醒：已就绪 · 请说「%s」", a.savedWakePhrase()), t.Success
	}
	ui.Row(c.Key("voice-wake-status")).Label("离线唤醒状态").Tooltip(detail).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(6, 6).Shrink(0).Radius(3).Background(color)
		ui.Text(c, wake).FontSize(11).TextColor(t.TextMuted).SingleLine()
	})
}

func (a *nativeUI) voiceHoldHintView(c *ui.Context) {
	t := c.Theme()
	key := a.voiceGlobalKey
	if key == "" {
		key = "F8"
	}
	hint, detail, color := key+" 按住说话", fmt.Sprintf("全局 %s · 后台也可按住说话 · 松开发送 · 最长 30 秒", key), t.TextMuted
	if !a.voiceGlobalReady {
		hint, detail = "快捷键未就绪", "全局快捷键尚未就绪"
	}
	if a.voiceHold {
		hint, detail, color = "录音中 · 松开发送", fmt.Sprintf("正在按住录音 · 松开 %s 发送 · 前台 Esc 取消", key), t.Accent
	}
	ui.Row(c.Key("voice-shortcut-hint")).Label("语音快捷键提示").Tooltip(detail).MinWidth(0).Grow(1).Children(func() {
		ui.Text(c, hint).MinWidth(0).Grow(1).FontSize(11).TextColor(color).SingleLine().Ellipsis("…")
	})
}

func (a *nativeUI) voicePreviewView(c *ui.Context) {
	t, state := c.Theme(), a.snapshot.Voice
	disabled := a.voiceDisabled()
	nativeSettingsTextArea(c, "voice-preview-field", "试听文本", func() ui.Element {
		return ui.TextInput(c.Key("voice-preview-text"), &a.voicePreview).Label("试听文本").Placeholder("输入试听文本，留空使用默认问候语").FillWidth().MinWidth(0).Height(36)
	})
	nativeSettingsActions(c, "voice-preview-actions", func() {
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
	nativeSettingsField(c, key, label, func() ui.Element {
		var field ui.Element
		ui.Row(c).FillWidth().MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
			visible := a.visibleKeys[key]
			field = ui.TextInput(c.Key("input"), value).Label(label).Placeholder(placeholder).Grow(1).MinWidth(0).Height(36)
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
		return field
	})
}
