package main

import (
	"strings"
	"testing"

	"aemeath/agent/internal/brain"

	"github.com/egoist/mygo/ui"
)

func TestNativeContextStatsRefreshWithoutVoiceEvent(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	f.state.Voice.Context = brain.ContextStats{ContextWindowSource: "provider", ContextWindowModel: strings.Repeat("very-long-request-model-id-", 8), EstimatedTokens: 21000, InputBudget: 28000, ContextWindow: 32768, UsagePercent: 75, Compressing: true, RetainedTurns: 4}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{"上下文（估算）：75.0% · 21000 / 28000 Tokens · 容量 32768", "摘要：正在压缩", "服务商尚未上报", "容量来源：服务上报"} {
		if !nativeContextHasText(tt.Texts(), text) {
			t.Fatalf("missing context display: %s", text)
		}
	}
	// Background compression produces no new voice turn or event.
	f.state.Voice.Context.Compressing = false
	f.state.Voice.Context.LastCompression = "compressed"
	f.state.Voice.Context.SummaryTokens = 500
	f.state.Voice.Context.Compressions = 1
	f.state.Voice.Context.UsageAvailable = true
	f.state.Voice.Context.PromptTokens = 12000
	f.state.Voice.Context.CompletionTokens = 700
	f.state.Voice.Context.TotalTokens = 12700
	a.refresh()
	tt.Frame()
	for _, text := range []string{"摘要：已生成摘要 · 500 Tokens", "累计 usage（服务商已上报）：输入 12000 · 输出 700 · 总计 12700 Tokens"} {
		if !nativeContextHasText(tt.Texts(), text) {
			t.Fatalf("background statistics did not refresh: %s", text)
		}
	}
	for _, label := range []string{"上下文与用量", "语音操作栏", "手动唤醒", "取消对话", "对话记录"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 || r.Y < 0 || r.Y+r.H > 600 {
			t.Fatalf("context display pushed controls out of compact window: %s %+v", label, r)
		}
	}
}

func nativeContextHasText(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

func TestNativeContextStatusesAndMissingUsage(t *testing.T) {
	for _, tc := range []struct{ status, want string }{{"", "尚未压缩"}, {"compressed", "已生成摘要"}, {"fallback", "已回退裁剪"}, {"failed", "压缩失败"}} {
		occupancy, summary, usage := nativeContextText(brain.ContextStats{LastCompression: tc.status}, 65536)
		if !strings.Contains(occupancy, "容量 65536 Tokens") || !strings.Contains(summary, tc.want) || !strings.Contains(usage, "尚未上报") || strings.Contains(usage, "总计 0") {
			t.Fatalf("incorrect pending statistics: %s / %s / %s", occupancy, summary, usage)
		}
	}
	_, summary, _ := nativeContextText(brain.ContextStats{SummaryTokens: 300}, 32768)
	if !strings.Contains(summary, "已有摘要") {
		t.Fatalf("restored summary reported as uncompressed: %s", summary)
	}
}

func TestNativeContextCapacitySources(t *testing.T) {
	model := strings.Repeat("provider/long-exact-model-id-", 8)
	for _, tc := range []struct{ source, mode, want string }{
		{"provider", "auto", "服务上报"}, {"manual", "manual", "手动设置"},
		{"fallback", "auto", "未获取，使用备用值"}, {"detecting", "auto", "等待识别"},
		{"", "", "等待识别"}, {"", "manual", "手动设置"},
	} {
		stats := brain.ContextStats{ContextWindowSource: tc.source, ContextWindowModel: model}
		source, tooltip := nativeContextSourceText(stats, tc.mode)
		if source != tc.want || !strings.Contains(tooltip, model) || strings.Contains(source, model) {
			t.Fatalf("source/model rendering: %q / %q", source, tooltip)
		}
	}
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "语音助手")
	for _, source := range []string{"provider", "manual", "fallback", "detecting"} {
		f.state.Voice.Context = brain.ContextStats{ContextWindowSource: source, ContextWindowModel: model, ContextWindow: 4096, InputBudget: 2253, EstimatedTokens: 1000}
		a.refresh()
		tt.Frame()
		want, _ := nativeContextSourceText(f.state.Voice.Context, "auto")
		if !nativeContextHasText(tt.Texts(), "容量来源："+want) || nativeContextHasText(tt.Texts(), model) {
			t.Fatalf("missing source or crowded model ID: %v", tt.Texts())
		}
		for _, label := range []string{"上下文与用量", "容量来源：" + want, "语音操作栏", "手动唤醒", "取消对话", "对话记录"} {
			r, ok := tt.Find(label)
			if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 || r.Y < 0 || r.Y+r.H > 600 {
				t.Fatalf("source %s escaped compact window: %s %+v", source, label, r)
			}
		}
	}
}

func TestNativeContextWindowSettingsSurviveRefreshAndSave(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "对话服务")
	nativeVoiceEdit(t, tt, "备用上下文容量（Tokens）", "131072")
	tt.Key(0, ui.KeyTab)
	if a.draft.Voice.ContextWindow != 131072 || !a.dirty {
		t.Fatalf("context capacity input was not bound: %d", a.draft.Voice.ContextWindow)
	}
	a.refresh()
	tt.Frame()
	if a.draft.Voice.ContextWindow != 131072 {
		t.Fatal("refresh discarded context capacity draft")
	}
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "自动读取模型上下文容量")
	if a.draft.Voice.ContextWindowMode != "manual" || !a.dirty {
		t.Fatal("manual context mode was not bound")
	}
	a.refresh()
	tt.Frame()
	if a.draft.Voice.ContextWindowMode != "manual" {
		t.Fatal("refresh discarded manual context mode")
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "手动上下文容量（Tokens）"); !ok {
		t.Fatal("manual mode did not relabel capacity")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Voice.ContextWindow != 131072 || f.saves[0].Voice.ContextWindowMode != "manual" {
		t.Fatal("manual context mode/capacity was not saved")
	}
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "自动读取模型上下文容量")
	if a.draft.Voice.ContextWindowMode != "auto" || a.draft.Voice.ContextWindow != 131072 {
		t.Fatal("switching to auto lost fallback capacity")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 2 || f.saves[1].Voice.ContextWindowMode != "auto" || f.saves[1].Voice.ContextWindow != 131072 {
		t.Fatal("auto mode/fallback capacity was not saved")
	}
}
