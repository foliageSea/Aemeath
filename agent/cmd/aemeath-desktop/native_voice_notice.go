package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

type nativeVoiceIssue struct {
	voiceError, shortcutError, notice string
	unsupported, failed               bool
	session                           uint64
	turn                              uint32
}

type nativeVoiceNoticeState struct {
	issue               nativeVoiceIssue
	expanded, dismissed bool
	scroll              ui.ScrollState
}

func (a *nativeUI) currentVoiceIssue() nativeVoiceIssue {
	_, supported := a.service.(nativeVoiceService)
	state := a.snapshot.Voice
	issue := nativeVoiceIssue{voiceError: state.Error, shortcutError: a.voiceGlobalError, notice: state.Notice, unsupported: !supported, failed: state.Phase == "error"}
	if issue.failed || issue.voiceError != "" {
		issue.session, issue.turn = state.Session, state.Turn
	}
	return issue
}

func (a *nativeUI) hasVoiceWarning() bool {
	issue := a.currentVoiceIssue()
	return issue.unsupported || issue.failed || issue.voiceError != "" || issue.shortcutError != ""
}

// Reset on recovery even when the voice page is not visible, so a later occurrence
// of the same error is not suppressed by an earlier dismissal.
func (a *nativeUI) syncVoiceNotice() {
	issue := a.currentVoiceIssue()
	if issue != a.voiceNotice.issue {
		a.voiceNotice = nativeVoiceNoticeState{issue: issue}
		a.voiceControlsHeight = 0
	}
}

func (a *nativeUI) voiceNoticeSummary() string {
	issue := a.voiceNotice.issue
	if issue.unsupported {
		return "当前服务不支持语音操作，请更新桌面应用。"
	}
	if !a.hasVoiceWarning() {
		return issue.notice
	}
	if !a.snapshot.Settings.Voice.Enabled {
		return "语音助手未启用，请在设置中启用。"
	}
	if !a.snapshot.Voice.Connected {
		return "语音暂不可用，请检查连接或语音设置。"
	}
	if issue.voiceError == "" && !issue.failed && issue.shortcutError != "" {
		return "语音快捷键不可用，可手动唤醒或发送文字消息。"
	}
	return "语音暂不可用，仍可发送文字消息"
}

func (a *nativeUI) voiceNoticeDetails() string {
	issue := a.voiceNotice.issue
	var details []string
	if issue.voiceError != "" {
		details = append(details, "原始信息："+issue.voiceError)
	}
	if issue.failed && issue.voiceError == "" {
		details = append(details, "语音状态异常，未收到具体错误信息。")
	}
	if issue.shortcutError != "" {
		details = append(details, "快捷键："+issue.shortcutError)
	}
	if issue.notice != "" && a.hasVoiceWarning() {
		details = append(details, issue.notice)
	}
	return strings.Join(details, "\n")
}

func (a *nativeUI) voiceNoticeView(c *ui.Context) {
	a.syncVoiceNotice()
	summary := a.voiceNoticeSummary()
	if summary == "" || a.voiceNotice.dismissed {
		return
	}
	t := c.Theme()
	color, label := t.Accent, "语音通知"
	if a.hasVoiceWarning() {
		color, label = t.Warning, "语音异常提示"
	}
	details := a.voiceNoticeDetails()
	ui.Column(c.Key("voice-notices")).Label(label).FillWidth().MinWidth(0).Shrink(0).Radius(10).Background(t.Surface).Border(1, color.Alpha(0.25)).Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(10, 12).Gap(8).Radius(9).Background(color.Alpha(0.06)).Children(func() {
			ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Grow(1).Basis(240).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "⚠").FontSize(13).TextColor(color).Shrink(0)
					ui.Text(c, summary).Grow(1).MinWidth(0).FontSize(12).TextColor(color).Wrap()
				})
				ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
					if details != "" {
						text := "查看详情"
						if a.voiceNotice.expanded {
							text = "收起详情"
						}
						ui.Button(c.Key("voice-notice-details"), text).Label("查看语音异常详情").Expanded(a.voiceNotice.expanded).FontSize(11).Padding(4, 6).Radius(5).Background(ui.Transparent).OnClick(func() { a.voiceNotice.expanded = !a.voiceNotice.expanded; a.voiceControlsHeight = 0; c.Invalidate() })
					}
					ui.ButtonBase(c.Key("voice-notice-dismiss")).Label("关闭语音异常提示").Size(24, 24).Radius(6).Center().Children(func() {
						ui.Icon(c, voiceCancelIcon).Size(13, 13).TextColor(t.TextMuted)
					}).OnClick(func() {
						a.voiceNotice.dismissed, a.voiceNotice.expanded = true, false
						a.voiceControlsHeight = 0
						c.Invalidate()
					})
				})
			})
			if a.voiceNotice.expanded && details != "" {
				ui.Row(c).FillWidth().MinWidth(0).Gap(8).AlignItems(ui.Start).Children(func() {
					// Cap long device/provider messages so details never push the composer off-screen.
					ui.Scroll(c.Key("voice-notice-detail-scroll")).Label("语音异常原始信息").Grow(1).MinWidth(0).MaxHeight(96).TrackScroll(&a.voiceNotice.scroll).Children(func() {
						ui.Text(c, details).FillWidth().MinWidth(0).Font("SF Mono, Menlo, Consolas, monospace").FontSize(11).TextColor(t.TextMuted).Wrap().Selectable()
					})
					ui.Button(c.Key("voice-notice-copy"), "复制").Label("复制语音异常信息").FontSize(11).Padding(4, 6).Shrink(0).OnClick(func() { c.WriteClipboard(details) })
				})
			}
		})
	})
}
