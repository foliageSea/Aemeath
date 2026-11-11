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
	f.state.Voice.Context = brain.ContextStats{EstimatedTokens: 21000, InputBudget: 28000, ContextWindow: 32768, UsagePercent: 75, Compressing: true, RetainedTurns: 4}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{"上下文（估算）：75.0% · 21000 / 28000 Tokens · 容量 32768", "摘要：正在压缩", "服务商尚未上报"} {
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

func TestNativeContextWindowSettingsSurviveRefreshAndSave(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "对话服务")
	nativeVoiceEdit(t, tt, "模型上下文容量（Tokens）", "131072")
	tt.Key(0, ui.KeyTab)
	if a.draft.Voice.ContextWindow != 131072 || !a.dirty {
		t.Fatalf("context capacity input was not bound: %d", a.draft.Voice.ContextWindow)
	}
	a.refresh()
	tt.Frame()
	if a.draft.Voice.ContextWindow != 131072 {
		t.Fatal("refresh discarded context capacity draft")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || f.saves[0].Voice.ContextWindow != 131072 {
		t.Fatal("context capacity was not saved")
	}
}
