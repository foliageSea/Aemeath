package main

import (
	"fmt"
	"math"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

func nativeToolName(name string) string {
	switch name {
	case "get_current_time":
		return "查询当前时间"
	case "web_search":
		return "网页搜索"
	case "get_metrics":
		return "查询系统指标"
	case "get_quota":
		return "查询额度用量"
	case "set_page":
		return "切换设备页面"
	case "set_screen":
		return "调整设备屏幕"
	case "lock_computer":
		return "锁定电脑"
	default:
		return name
	}
}

func nativeVoiceActivity(state voice.State) string {
	for _, call := range state.ToolCalls {
		if call.Status == "running" {
			return "正在调用：" + nativeToolName(call.Name)
		}
	}
	return nativeVoicePhase(state.Phase)
}

func nativeToolStatus(status string) string {
	switch status {
	case "running":
		return "执行中"
	case "succeeded":
		return "已完成"
	case "failed":
		return "失败"
	case "canceled":
		return "已取消"
	case "unknown":
		return "已结束"
	default:
		return "等待执行"
	}
}

func nativeVoiceMillis(millis int64) string {
	if millis <= 0 {
		return "—"
	}
	if millis >= 1000 {
		return fmt.Sprintf("%.2f s", float64(millis)/1000)
	}
	return fmt.Sprintf("%d ms", millis)
}

func nativeVoicePlayback(state voice.State) string {
	if state.GeneratedSegments == 0 {
		if state.Phase == "synthesizing" {
			return "正在合成首段语音"
		}
		if state.Phase == "speaking" {
			return "正在播放音频"
		}
		return "尚未生成语音"
	}
	// The final segment count can still grow while generation and playback overlap.
	return fmt.Sprintf("已播放 %d 段 · 已合成 %d 段", state.PlayedSegments, state.GeneratedSegments)
}

func (a *nativeUI) voiceLiveProgressView(c *ui.Context) {
	t, state := c.Theme(), a.snapshot.Voice
	ui.Text(c, fmt.Sprintf("第 %d 轮 · %s", state.Turn, nativeVoicePlayback(state))).FontSize(11).TextColor(t.TextMuted).Wrap()
	if state.GeneratedSegments > 0 {
		ui.Progress(c.Key("voice-live-playback"), math.Min(1, float64(state.PlayedSegments)/float64(state.GeneratedSegments))).Label("当前语音播放进度").FillWidth().Height(4)
	}
}

func (a *nativeUI) voiceTurnProcessView(c *ui.Context, index int) {
	turn := &a.voiceHistory[index]
	state := turn.state
	t := c.Theme()
	ui.Column(c.Key("voice-process")).Label(fmt.Sprintf("第 %d 轮执行过程", turn.turn)).FillWidth().MinWidth(0).Padding(12, 14).Gap(10).Radius(12).Border(1, t.Border.Alpha(0.6)).Background(t.Surface.Alpha(0.55)).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "执行过程").FontSize(11).TextColor(t.TextMuted).Grow(1)
			if len(state.ToolCalls) > 0 {
				ui.Text(c, fmt.Sprintf("%d 次工具调用", len(state.ToolCalls))).FontSize(11).TextColor(t.TextMuted)
			}
		})
		for _, call := range state.ToolCalls {
			a.voiceToolCallView(c, index, call)
		}
		ui.Row(c).FillWidth().Gap(12).Children(func() {
			for _, timing := range []struct {
				name   string
				millis int64
			}{{"语音识别", state.ASRMillis}, {"思考", state.AgentMillis}, {"语音合成", state.TTSMillis}} {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, timing.name).FontSize(10).TextColor(t.TextMuted)
					ui.Text(c, nativeVoiceMillis(timing.millis)).FontSize(12).Bold()
				})
			}
		})
		if state.FirstAudioMillis > 0 {
			ui.Text(c, "首段播放耗时 "+nativeVoiceMillis(state.FirstAudioMillis)).FontSize(11).TextColor(t.TextMuted)
		}
		if state.GeneratedSegments > 0 {
			ui.Text(c, nativeVoicePlayback(state)).FontSize(11).TextColor(t.TextMuted).Wrap()
			ui.Progress(c, math.Min(1, float64(state.PlayedSegments)/float64(state.GeneratedSegments))).Label(fmt.Sprintf("第 %d 轮播放进度", turn.turn)).FillWidth().Height(4)
		}
	})
}

func (a *nativeUI) voiceToolCallView(c *ui.Context, index int, call brain.ToolCall) {
	turn := &a.voiceHistory[index]
	t := c.Theme()
	color := t.TextMuted
	switch call.Status {
	case "running":
		color = t.Accent
	case "succeeded":
		color = t.Success
	case "failed":
		color = t.Warning
	}
	expanded := turn.toolDetails[call.ID]
	ui.Column(c.Key(fmt.Sprintf("voice-tool-%d", call.ID))).FillWidth().MinWidth(0).Gap(8).Children(func() {
		button := ui.ButtonBase(c.Key("toggle")).Label(fmt.Sprintf("第 %d 轮工具详情 %s #%d", turn.turn, nativeToolName(call.Name), call.ID)).Expanded(expanded).FillWidth().Gap(8).AlignItems(ui.Center).Justify(ui.Start).Padding(9, 10).Radius(8).Background(color.Alpha(0.08)).Border(1, color.Alpha(0.15))
		button.Children(func() {
			arrow := "›"
			if expanded {
				arrow = "⌄"
			}
			ui.Text(c, arrow).FontSize(14).TextColor(color)
			ui.Text(c, nativeToolName(call.Name)).FontSize(12).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			ui.Text(c, nativeToolStatus(call.Status)).FontSize(11).TextColor(color)
			if call.Millis > 0 {
				ui.Text(c, nativeVoiceMillis(call.Millis)).FontSize(11).TextColor(t.TextMuted)
			}
		}).OnClick(func() {
			entry := &a.voiceHistory[index]
			if entry.toolDetails == nil {
				entry.toolDetails = map[uint64]bool{}
			}
			entry.toolDetails[call.ID] = !entry.toolDetails[call.ID]
		})
		if call.Name == "get_current_time" && !expanded {
			if report, ok := brain.ParseCurrentTimeReport(call.Result); ok && report.OK {
				ui.Text(c, report.Summary()).FontSize(11).TextColor(t.TextMuted).Wrap()
				if report.Info.Source == "system_fallback" {
					ui.Text(c, "电脑系统时间 · 外部时间暂不可用").FontSize(11).TextColor(t.Warning).Wrap()
				}
			}
		}
		if call.Name == "web_search" && !expanded {
			if report, ok := brain.ParseWebSearchReport(call.Result); ok && report.OK {
				summary := report.Summary()
				if len(report.Results) > 0 {
					summary += " · 展开查看来源"
				}
				ui.Text(c, summary).FontSize(11).TextColor(t.TextMuted).Wrap()
			}
		}
		if expanded {
			ui.Column(c).FillWidth().MinWidth(0).Padding(4, 10, 8).Gap(6).Children(func() {
				ui.Text(c, call.Name).FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, "参数").FontSize(11).Bold()
				args := call.Arguments
				if args == "" {
					args = "无参数"
				}
				ui.Text(c, args).FontSize(11).TextColor(t.TextMuted).Wrap().Selectable()
				ui.Text(c, "结果").FontSize(11).Bold()
				result := call.Result
				if result == "" {
					result = nativeToolStatus(call.Status)
					if call.Status == "running" {
						result = "等待工具返回…"
					}
				}
				if call.Name == "get_current_time" {
					if report, ok := brain.ParseCurrentTimeReport(result); ok && report.OK {
						a.voiceCurrentTimeView(c, report)
					} else {
						ui.Text(c, result).FontSize(11).Wrap().Selectable()
					}
				} else if call.Name == "web_search" {
					if report, ok := brain.ParseWebSearchReport(result); ok && report.OK {
						a.voiceWebSearchSourcesView(c, report)
					} else {
						ui.Text(c, result).FontSize(11).Wrap().Selectable()
					}
				} else {
					ui.Text(c, result).FontSize(11).Wrap().Selectable()
				}
			})
		}
	})
}
