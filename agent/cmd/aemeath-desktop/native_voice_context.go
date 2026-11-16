package main

import (
	"fmt"
	"strings"

	"aemeath/agent/internal/brain"

	"github.com/egoist/mygo/ui"
)

func nativeContextWindow(stats brain.ContextStats, configuredWindow int) int {
	window := stats.ContextWindow
	if window == 0 {
		window, _ = brain.NormalizeContextWindow(configuredWindow)
	}
	return window
}

func nativeContextNumber(value int) string {
	text := fmt.Sprint(value)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}

func nativeContextCapacity(window int) string {
	if window >= 1024 && window%1024 == 0 {
		return fmt.Sprintf("%dK", window/1024)
	}
	return nativeContextNumber(window)
}

func nativeContextSummary(stats brain.ContextStats) string {
	status := "尚未压缩"
	if stats.SummaryTokens > 0 {
		status = "已有摘要"
	}
	switch stats.LastCompression {
	case "compressed":
		status = "已生成摘要"
	case "fallback":
		status = "已回退裁剪"
	case "failed":
		status = "压缩失败"
	}
	if stats.Compressing {
		status = "正在压缩"
	}
	return status
}

func nativeContextSourceText(stats brain.ContextStats, configuredMode string) string {
	var source string
	switch stats.ContextWindowSource {
	case "modelsdev":
		source = "models.dev"
	case "manual":
		source = "手动设置"
	case "fallback":
		source = "未获取，使用备用值"
	default:
		source = "未获取，使用备用值"
		if configuredMode == "manual" {
			source = "手动设置"
		}
	}
	return source
}

func (a *nativeUI) voiceContextView(c *ui.Context, details bool) {
	stats := a.snapshot.Voice.Context
	window := nativeContextWindow(stats, a.snapshot.Settings.Voice.ContextWindow)
	summary := nativeContextSummary(stats)
	source := nativeContextSourceText(stats, a.snapshot.Settings.Voice.ContextWindowMode)
	t := c.Theme()
	occupancyColor, summaryColor := t.Accent, t.TextMuted
	if stats.UsagePercent >= 85 {
		occupancyColor = t.Warning
	}
	if stats.Compressing {
		summaryColor = t.Accent
	} else if stats.LastCompression == "failed" || stats.LastCompression == "fallback" {
		summaryColor = t.Warning
	}
	if !details {
		ui.Row(c.Key("voice-context")).Label("上下文与用量").FillWidth().MinWidth(0).Gap(12).AlignItems(ui.Center).Children(func() {
			status := a.voiceComposerStatus()
			ui.Row(c.Key("voice-footer-status")).Label("当前对话状态").Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Shrink(0).Radius(3).Background(nativeVoicePhaseColor(t, a.snapshot.Voice))
				ui.Text(c, status).FontSize(11).TextColor(t.TextMuted).MinWidth(0).SingleLine().Ellipsis("…")
			})
			ui.Row(c).Shrink(0).MinWidth(0).Wrap().GapX(10).GapY(6).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "上下文").FontSize(12).TextColor(t.TextMuted)
					if stats.InputBudget > 0 {
						ui.Text(c, fmt.Sprintf("%.1f%%", stats.UsagePercent)).FontSize(12).Bold().TextColor(occupancyColor)
					} else {
						ui.Text(c, "等待初始化").FontSize(12).TextColor(t.TextMuted)
					}
				})
				if summary != "尚未压缩" {
					ui.Text(c, summary).FontSize(12).TextColor(summaryColor)
				} else if stats.InputBudget > 0 && stats.UsagePercent >= 70 {
					ui.Text(c, "接近压缩阈值").FontSize(12).TextColor(t.Warning)
				}
			})
			button := ui.ButtonBase(c.Key("details-toggle")).Label("上下文详情").Expanded(a.voiceContextExpanded).Shrink(0).Padding(4, 6).Gap(3).Radius(6).AlignItems(ui.Center)
			if button.Hovered() {
				button.Background(t.SurfaceHover)
			}
			button.Children(func() {
				label, icon := "详情", voiceToolCollapsedIcon
				if a.voiceContextExpanded {
					label, icon = "收起", voiceToolExpandedIcon
				}
				ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
				ui.Icon(c, icon).Size(12, 12).TextColor(t.TextMuted)
			}).OnClick(func() { a.voiceContextExpanded = !a.voiceContextExpanded })
		})
		return
	}
	if !a.voiceContextExpanded {
		return
	}
	ui.Column(c.Key("details")).Label("上下文统计详情").FillWidth().MinWidth(0).Padding(8, 16, 10).Gap(7).BorderWidth(1, 0, 0, 0).BorderColor(t.Border.Alpha(0.35)).Children(func() {
		occupancy := "等待模型初始化"
		if stats.InputBudget > 0 {
			occupancy = fmt.Sprintf("%s / %s Tokens（估算）", nativeContextNumber(stats.EstimatedTokens), nativeContextNumber(stats.InputBudget))
		}
		state := a.snapshot.Voice
		if state.Turn > 0 && !(a.voiceCleared && state.Turn == a.voiceClearTurn && state.Session == a.voiceClearSession) {
			a.voiceLiveStatusView(c)
		}
		nativeContextDetail(c, "上下文", occupancy, t.Text)
		nativeContextDetail(c, "容量", nativeContextCapacity(window)+" · "+strings.ReplaceAll(source, "未获取，使用备用值", "使用备用值"), t.Text)
		summaryDetail := summary
		if stats.SummaryTokens > 0 {
			summaryDetail += " · " + nativeContextNumber(stats.SummaryTokens) + " Tokens"
		}
		nativeContextDetail(c, "摘要", summaryDetail, summaryColor)
		nativeContextDetail(c, "长期记忆", fmt.Sprintf("%d 条 · 召回 %d 条 · %d Tokens（估算）", stats.MemoryEntries, stats.RecalledMemories, stats.MemoryTokens), t.TextMuted)
		nativeContextDetail(c, "对话保留", fmt.Sprintf("%d 轮 · 压缩 %d 次 · 丢弃 %d 轮", stats.RetainedTurns, stats.Compressions, stats.DroppedTurns), t.TextMuted)
		usage := fmt.Sprintf("输入 %s · 输出 %s · 总计 %s Tokens（本地估算）", nativeContextNumber(stats.LocalPromptTokens), nativeContextNumber(stats.LocalCompletionTokens), nativeContextNumber(stats.LocalTotalTokens))
		nativeContextDetail(c, "累计用量", usage, t.Text)
	})
}

func nativeContextDetail(c *ui.Context, label, value string, color ui.Color) {
	ui.Row(c).FillWidth().MinWidth(0).Gap(12).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).Width(60).Shrink(0).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Text(c, value).Grow(1).MinWidth(0).FontSize(12).TextColor(color).Wrap()
	})
}
