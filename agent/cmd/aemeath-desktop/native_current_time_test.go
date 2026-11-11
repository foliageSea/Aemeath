package main

import (
	"encoding/json"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/timeaware"
	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

func currentTimeCardFixture(t *testing.T, source string) brain.CurrentTimeReport {
	t.Helper()
	instant, err := time.Parse(time.RFC3339, "2026-11-10T09:30:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	info := timeaware.Info{DateTime: instant.Format(time.RFC3339), UTC: instant.UTC().Format(time.RFC3339), Unix: instant.Unix(), Timezone: "Asia/Shanghai", UTCOffset: "+08:00", Weekday: "星期二", Date: "2026-11-10", Time: "09:30:00", Source: source, Provider: "timeapi.io"}
	if source == "system_fallback" {
		info.Provider = "system"
		info.Notice = "外部时间服务不可用，使用电脑系统时间"
	}
	return brain.CurrentTimeReport{OK: true, Info: info}
}

func TestNativeCurrentTimeShowsDateTimezoneAndActualSource(t *testing.T) {
	for _, source := range []string{"network", "system_fallback"} {
		t.Run(source, func(t *testing.T) {
			a, f, _, tt := nativeTest(t)
			tt.SetSize(900, 1050)
			report := currentTimeCardFixture(t, source)
			data, _ := json.Marshal(report)
			f.state.Voice = voice.State{Turn: 1, Transcript: "现在北京时间几点？", Response: "现在是上午九点半。", ToolCalls: []brain.ToolCall{{ID: 1, Name: "get_current_time", Status: "succeeded", Result: string(data), Millis: 180}}}
			a.refresh()
			nativeClick(t, tt, "语音助手")
			if !tt.HasText("查询当前时间") || !tt.HasText(report.Summary()) {
				t.Fatal("time tool not represented in conversation")
			}
			nativeClick(t, tt, "第 1 轮工具详情 查询当前时间 #1")
			for _, text := range []string{"查询时刻", "09:30:00", "2026-11-10 · 星期二", "Asia/Shanghai · UTC+08:00", "UTC：2026-11-10T01:30:00Z"} {
				if !tt.HasText(text) {
					t.Fatalf("missing time field %s", text)
				}
			}
			if source == "network" {
				if !tt.HasText("外部时间 · timeapi.io") {
					t.Fatal("missing external source")
				}
			} else {
				if !tt.HasText(report.Info.Notice) || tt.HasText("外部时间 · timeapi.io") {
					t.Fatal("system fallback pretends to be external time")
				}
			}
			f.state.Voice = voice.State{Turn: 2, Transcript: "下一问", Response: "下一答"}
			a.refresh()
			tt.Frame()
			if !tt.HasText("09:30:00") {
				t.Fatal("new turn overwrote historical time observation")
			}
		})
	}
}

func TestNativeTimeCardKeepsCompactControlsVisible(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 577)
	report := currentTimeCardFixture(t, "system_fallback")
	data, _ := json.Marshal(report)
	f.state.Voice = voice.State{Turn: 1, Phase: "thinking", Transcript: "查询现在时间", ToolCalls: []brain.ToolCall{{ID: 1, Name: "get_current_time", Status: "succeeded", Result: string(data), Millis: 4000}}}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "第 1 轮工具详情 查询当前时间 #1")
	for _, label := range []string{"语音操作栏", "手动唤醒", "取消对话"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 226 || r.X+r.W > 740 || r.Y+r.H > 557 {
			t.Fatalf("time card displaced control %s: %+v", label, r)
		}
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "第 1 轮工具详情 查询当前时间 #1"); !ok {
		t.Fatal("missing time details control")
	}
}
