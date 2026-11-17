package main

import (
	"aemeath/agent/internal/desktop"
	"github.com/egoist/mygo/ui"
)

var voiceSpeakerIcon = sidebarSVG("<path d='M11 5 6 9H3v6h3l5 4V5ZM15 8a6 6 0 0 1 0 8M18 5a10 10 0 0 1 0 14'/>")

func (a *nativeUI) voiceHeaderView(c *ui.Context) {
	t, state := c.Theme(), a.snapshot.Voice
	// Keep actions separate; only the identity badges and route may wrap within the flexible left column.
	ui.Row(c.Key("voice-toolbar")).Label("语音助手状态栏").FillWidth().MinWidth(0).Shrink(0).Gap(12).Padding(0, 0, 12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border.Alpha(0.5)).AlignItems(ui.Center).Children(func() {
		ui.Row(c.Key("voice-identity")).Grow(1).Shrink(1).MinWidth(0).Gap(10).AlignItems(ui.Center).Children(func() {
			voiceAvatar(c, 40)
			ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
				ui.Row(c.Key("voice-connection")).Label("助手连接状态").FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "爱弥斯").Bold().FontSize(15).Shrink(0)
					connection, color := "未连接", t.TextMuted
					if state.Connected {
						connection, color = "已连接", t.Success
					}
					ui.Row(c.Key("voice-connected-badge")).Shrink(0).Padding(3, 7).Gap(5).Radius(6).Background(color.Alpha(0.10)).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(5, 5).Radius(3).Background(color)
						ui.Text(c, connection).FontSize(10).TextColor(color).SingleLine()
					})
					if a.hasVoiceWarning() {
						badge := ui.ButtonBase(c.Key("voice-health")).Label("查看语音状态").Shrink(0).Padding(3, 7).Radius(6).Background(t.Warning.Alpha(0.10))
						if badge.Hovered() {
							badge.Background(t.Warning.Alpha(0.18))
						}
						badge.Children(func() { ui.Text(c, "语音异常").FontSize(10).TextColor(t.Warning) }).OnClick(func() {
							a.voiceNotice.dismissed, a.voiceNotice.expanded = false, true
							a.voiceControlsHeight = 0
							c.Invalidate()
						})
					} else if state.Connected && a.snapshot.Settings.Voice.Enabled {
						ui.Text(c, nativeVoicePhase(state.Phase)).FontSize(11).TextColor(nativeVoicePhaseColor(t, state)).SingleLine()
					}
				})
				a.voicePathView(c)
				ui.Row(c.Key("voice-header-hints")).Label("助手唤醒与快捷键").FillWidth().MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
					a.voiceStatusView(c)
					ui.Text(c, "·").FontSize(11).TextColor(t.TextMuted).Shrink(0)
					a.voiceHoldHintView(c)
				})
			})
		})
		ui.Row(c.Key("voice-header-actions")).Label("语音快捷操作").Shrink(0).Gap(10).AlignItems(ui.Center).Children(func() {
			a.voicePlaybackOutputView(c)
			voiceIconButton(c, "voice-configure", "语音设置", voiceSettingsIcon, false, false, a.openVoiceSettings)
			voiceIconButton(c, "voice-clear", "清空对话", voiceClearIcon, false, a.voiceDisabled(), func() { a.voiceClearConfirm = true })
		})
	})
}

func (a *nativeUI) voicePathView(c *ui.Context) {
	t := c.Theme()
	input, output := "ESP32 麦克风", "ESP32 扬声器"
	standalone := a.snapshot.Settings.RunMode == desktop.RunModeStandalone
	if standalone {
		input = "本机麦克风"
	}
	if standalone || a.snapshot.Settings.Voice.PlaybackOutput == "console" {
		output = "本机扬声器"
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
		ui.Row(c.Key("voice-path")).Label(a.voicePathLabel()).FillWidth().MinWidth(0).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, sidebarVoice).Size(12, 12).TextColor(t.TextMuted)
				ui.Text(c, input).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			ui.Text(c, "→").FontSize(11).TextColor(t.TextMuted)
			ui.Row(c).Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, voiceSpeakerIcon).Size(12, 12).TextColor(t.TextMuted)
				ui.Text(c, output).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		})
		if standalone && a.snapshot.Voice.Phase == "speaking" {
			ui.Text(c, "播放时暂停自动唤醒，可手动打断").FontSize(10).TextColor(t.TextMuted).Wrap()
		}
	})
}
