package main

import (
	"fmt"

	"aemeath/agent/internal/brain"

	"github.com/egoist/mygo/ui"
)

// Keep estimates and provider-reported usage separate: they measure different
// things, and many compatible providers never send usage at all.
func nativeContextText(stats brain.ContextStats, configuredWindow int) (occupancy, summary, usage string) {
	window := stats.ContextWindow
	if window == 0 {
		window, _ = brain.NormalizeContextWindow(configuredWindow)
	}
	if stats.InputBudget > 0 {
		occupancy = fmt.Sprintf("上下文（估算）：%.1f%% · %d / %d Tokens · 容量 %d", stats.UsagePercent, stats.EstimatedTokens, stats.InputBudget, window)
	} else {
		occupancy = fmt.Sprintf("上下文（估算）：等待模型初始化 · 容量 %d Tokens", window)
	}
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
	summary = fmt.Sprintf("摘要：%s · %d Tokens · 保留 %d 轮 · 压缩 %d 次 · 丢弃 %d 轮", status, stats.SummaryTokens, stats.RetainedTurns, stats.Compressions, stats.DroppedTurns)
	usage = "累计 usage：服务商尚未上报（部分服务商不支持）"
	if stats.UsageAvailable {
		usage = fmt.Sprintf("累计 usage（服务商已上报）：输入 %d · 输出 %d · 总计 %d Tokens", stats.PromptTokens, stats.CompletionTokens, stats.TotalTokens)
	}
	return
}

func (a *nativeUI) voiceContextView(c *ui.Context) {
	stats := a.snapshot.Voice.Context
	occupancy, summary, usage := nativeContextText(stats, a.snapshot.Settings.Voice.ContextWindow)
	t := c.Theme()
	color := t.TextMuted
	if stats.UsagePercent >= 85 || stats.LastCompression == "failed" {
		color = t.Warning
	} else if stats.Compressing {
		color = t.Accent
	}
	ui.Column(c.Key("voice-context")).Label("上下文与用量").FillWidth().MinWidth(0).Gap(3).Children(func() {
		ui.Text(c, occupancy).FontSize(11).TextColor(color).Wrap().Tooltip(fmt.Sprintf("按序列化请求估算，非精确分词；输入预算已扣除 %d Tokens 预留。达到 70%% 后台摘要，85%% 同步压缩。", stats.ReservedTokens))
		ui.Text(c, summary).FontSize(11).TextColor(color).Wrap()
		ui.Text(c, usage).FontSize(11).TextColor(t.TextMuted).Wrap().Tooltip("当前会话中服务商已上报的累计数据，可能包含工具决策、最终回复与摘要请求；未上报部分无法计入，重启应用后重新累计。")
	})
}
