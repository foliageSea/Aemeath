package main

import (
	"testing"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

func TestNativeVoiceToolLifecycleAndTimingRefreshWithoutTextChanges(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(1100, 1400)
	a.service = &nativeVoiceFake{nativeFake: f}
	f.state.Settings.Voice.Enabled = true
	f.state.Voice = voice.State{Connected: true, WakeReady: true, Turn: 1, Phase: "thinking", Transcript: "现在CPU忙吗？", ASRMillis: 123, ToolCalls: []brain.ToolCall{{ID: 1, Name: "get_metrics", Arguments: "{}", Status: "running"}}}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{"查询系统指标", "执行中", "123 ms", "正在调用：查询系统指标"} {
		if !tt.HasText(text) {
			t.Fatalf("missing live tool/timing state: %s", text)
		}
	}
	label := "第 1 轮工具详情 查询系统指标 #1"
	nativeClick(t, tt, label)
	if !tt.HasText("等待工具返回…") || !tt.HasText("get_metrics") {
		t.Fatal("tool details unavailable")
	}
	f.state.Voice.ToolCalls[0].Status = "succeeded"
	f.state.Voice.ToolCalls[0].Millis = 45
	f.state.Voice.ToolCalls[0].Result = "已读取电脑当前系统指标"
	f.state.Voice.Response = "CPU现在不忙。"
	f.state.Voice.Phase = "speaking"
	f.state.Voice.AgentMillis = 1500
	f.state.Voice.TTSMillis = 600
	f.state.Voice.FirstAudioMillis = 2345
	f.state.Voice.GeneratedSegments = 3
	f.state.Voice.PlayedSegments = 1
	a.refresh()
	tt.Frame()
	for _, text := range []string{"已完成", "45 ms", "1.50 s", "600 ms", "首段播放耗时 2.35 s", "已播放 1 段 · 已合成 3 段", "已读取电脑当前系统指标"} {
		if !tt.HasText(text) {
			t.Fatalf("missing completed tool or audio diagnostics: %s", text)
		}
	}
	info, ok := tt.AccessByRole(ui.RoleButton, label)
	if !ok || !info.Expanded {
		t.Fatal("state refresh collapsed tool details")
	}
	f.state.Voice.PlayedSegments = 2
	f.state.Voice.TTSMillis = 780
	a.refresh()
	tt.Frame()
	if len(a.voiceHistory) != 1 || a.voiceHistory[0].state.PlayedSegments != 2 || !tt.HasText("780 ms") {
		t.Fatal("non-text progress failed to update")
	}
	for _, label := range []string{"当前语音播放进度", "第 1 轮播放进度"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("obsolete playback progress remains: %s", label)
		}
	}
}

func TestNativeVoiceToolDetailsAndMetricsBelongToTheirOwnTurns(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(1100, 1800)
	f.state.Voice = voice.State{Turn: 1, Phase: "idle", Transcript: "第一问", Response: "第一答", ASRMillis: 125, AgentMillis: 240, TTSMillis: 360, GeneratedSegments: 2, PlayedSegments: 2, ToolCalls: []brain.ToolCall{{ID: 1, Name: "set_page", Arguments: `{"page":"quota"}`, Result: "切页指令已发送", Status: "succeeded", Millis: 12}}}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "第 1 轮工具详情 切换设备页面 #1")
	f.state.Voice = voice.State{Turn: 2, Phase: "thinking", Transcript: "第二问", ToolCalls: []brain.ToolCall{{ID: 1, Name: "set_page", Arguments: `{"page":"metrics"}`, Status: "running"}}}
	a.refresh()
	tt.Frame()
	if len(a.voiceHistory) != 2 || a.voiceHistory[0].state.ASRMillis != 125 || a.voiceHistory[0].state.PlayedSegments != 2 || a.voiceHistory[0].state.ToolCalls[0].Status != "succeeded" {
		t.Fatal("new turn overwrote previous timing or tool result")
	}
	second, ok := tt.AccessByRole(ui.RoleButton, "第 2 轮工具详情 切换设备页面 #1")
	if !ok || second.Expanded {
		t.Fatal("tool expansion leaked across turns with reused IDs")
	}
	f.state.Voice.ToolCalls[0].Status = "canceled"
	a.refresh()
	tt.Frame()
	if !tt.HasText("已取消") || a.voiceHistory[0].state.ToolCalls[0].Status != "succeeded" {
		t.Fatal("canceled tool modified historical tool state")
	}
	f.state.Voice.ToolCalls[0].Status = "failed"
	a.refresh()
	tt.Frame()
	if !tt.HasText("失败") {
		t.Fatal("tool failure displayed as success")
	}
}

func TestNativeVoiceSkippedToolCompletionDoesNotStayRunning(t *testing.T) {
	a, f, _, _ := nativeTest(t)
	f.state.Voice = voice.State{Turn: 1, Phase: "thinking", Transcript: "第一问", ToolCalls: []brain.ToolCall{{ID: 1, Name: "get_metrics", Status: "running"}}}
	a.refresh()
	f.state.Voice = voice.State{Turn: 2, Phase: "thinking", Transcript: "第二问"}
	a.refresh()
	call := a.voiceHistory[0].state.ToolCalls[0]
	if call.Status != "unknown" || call.Result == "" {
		t.Fatal("missed completion incorrectly displayed as still running or successful")
	}
}

func TestNativeVoiceFailureRemainsInHistoricalExecutionCard(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(1100, 1800)
	f.state.Settings.Voice.Enabled = true
	f.state.Voice = voice.State{Connected: true, Session: 1, Turn: 1, Phase: "error", ErrorStage: "synthesizing", Error: "语音服务暂时不可用 [http_error] HTTP 503", Transcript: "第一问", Response: "第一答", AgentMillis: 120, TTSMillis: 250}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{"语音处理失败", "失败阶段：语音合成", "失败原因：" + f.state.Voice.Error, "第一答", "250 ms"} {
		if !tt.HasText(text) {
			t.Fatalf("missing voice failure diagnostics: %s", text)
		}
	}
	f.state.Voice = voice.State{Session: 1, Turn: 2, Phase: "thinking", Transcript: "第二问"}
	a.refresh()
	tt.Frame()
	if len(a.voiceHistory) != 2 || a.voiceHistory[0].state.ErrorStage != "synthesizing" || !tt.HasText("失败阶段：语音合成") || !tt.HasText("失败原因：语音服务暂时不可用 [http_error] HTTP 503") {
		t.Fatal("new turn lost historical failure details")
	}
}

func TestNativeVoiceFailureUnknownStageAndMissingMessage(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Voice = voice.State{Turn: 1, Phase: "error", Transcript: "测试"}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if !tt.HasText("失败阶段：未知阶段") || !tt.HasText("失败原因：未收到具体错误信息。") {
		t.Fatal("missing explicit fallback failure diagnostics")
	}
	for _, stage := range []string{"", "private_backend_value"} {
		if got := nativeVoiceStage(stage); got != "未知阶段" {
			t.Fatalf("stage %q = %q", stage, got)
		}
	}
	for stage, want := range map[string]string{"recognizing": "语音识别", "thinking": "思考 / 工具调用", "synthesizing": "语音合成", "speaking": "语音播放", "listening": "录音", "wake": "唤醒", "connecting": "语音连接"} {
		if got := nativeVoiceStage(stage); got != want {
			t.Fatalf("stage %q = %q, want %q", stage, got, want)
		}
	}
	if got := nativeVoiceActivity(voice.State{Phase: "error", ToolCalls: []brain.ToolCall{{Name: "get_metrics", Status: "running"}}}); got != "语音处理失败" {
		t.Fatalf("stale tool hides current failure: %s", got)
	}
}

func TestNativeVoiceWakeDiagnosticIsNotTurnFailure(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	f.state.Voice = voice.State{Turn: 1, Phase: "speaking", Transcript: "测试", Response: "回复", Error: "Offline wake unavailable"}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if tt.HasText("失败阶段：未知阶段") || tt.HasText("失败原因：Offline wake unavailable") {
		t.Fatal("nonfatal wake diagnostic became a turn failure")
	}
}

func TestNativeVoiceProcessSmallWindowKeepsControlsVisible(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, f, _, tt := nativeTest(t)
		tt.SetSize(760, 577)
		tt.SetDark(dark)
		f.state.Voice = voice.State{Connected: true, WakeReady: true, Turn: 1, Phase: "speaking", Transcript: "看看系统指标", Response: "当前CPU使用率正常。", ASRMillis: 120, AgentMillis: 340, TTSMillis: 560, FirstAudioMillis: 1234, GeneratedSegments: 4, PlayedSegments: 1, ToolCalls: []brain.ToolCall{{ID: 1, Name: "get_metrics", Arguments: "{}", Result: "已读取系统指标", Status: "succeeded", Millis: 12}}}
		a.refresh()
		nativeClick(t, tt, "语音助手")
		for _, label := range []string{"语音操作栏", "手动唤醒", "取消对话"} {
			r, ok := tt.Find(label)
			if !ok || r.W <= 0 || r.X < 226 || r.X+r.W > 740 || r.Y < 0 || r.Y+r.H > 557 {
				t.Fatalf("process UI displaced control dark=%v %s: %+v", dark, label, r)
			}
		}
	}
}
