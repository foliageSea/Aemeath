package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aemeath/agent/internal/brain"

	"github.com/egoist/mygo/ui"
)

func TestNativeContextStatsRefreshWithoutVoiceEvent(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	f.state.Voice.Context = brain.ContextStats{ContextWindowSource: "modelsdev", ContextWindowModel: strings.Repeat("very-long-request-model-id-", 8), EstimatedTokens: 21000, InputBudget: 28000, ContextWindow: 32768, UsagePercent: 75, Compressing: true, RetainedTurns: 4}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{"75.0%", "正在压缩", "容量 32K"} {
		if !nativeContextHasText(tt.Texts(), text) {
			t.Fatalf("missing context display: %s", text)
		}
	}
	if _, ok := tt.Find("上下文统计详情"); ok {
		t.Fatal("context details should be collapsed by default")
	}
	nativeClick(t, tt, "上下文详情")
	for _, text := range []string{"21,000 / 28,000 Tokens（估算）", "输入 0 · 输出 0 · 总计 0 Tokens（本地估算）", "32K · models.dev"} {
		if !nativeContextHasText(tt.Texts(), text) {
			t.Fatalf("missing expanded context display: %s", text)
		}
	}
	// Background compression produces no new voice turn or event.
	f.state.Voice.Context.Compressing = false
	f.state.Voice.Context.LastCompression = "compressed"
	f.state.Voice.Context.SummaryTokens = 500
	f.state.Voice.Context.Compressions = 1
	// Local estimates refresh even when the provider has not reported usage.
	f.state.Voice.Context.LocalPromptTokens = 12000
	f.state.Voice.Context.LocalCompletionTokens = 700
	f.state.Voice.Context.LocalTotalTokens = 12700
	a.refresh()
	tt.Frame()
	for _, text := range []string{"已生成摘要 · 500 Tokens", "输入 12,000 · 输出 700 · 总计 12,700 Tokens（本地估算）"} {
		if !nativeContextHasText(tt.Texts(), text) {
			t.Fatalf("background statistics did not refresh: %s", text)
		}
	}
	for _, label := range []string{"上下文与用量", "上下文统计详情", "语音操作栏", "手动唤醒", "取消对话", "对话记录"} {
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

func TestNativeContextStatusesAndZeroLocalUsage(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	nativeClick(t, tt, "语音助手")
	nativeClick(t, tt, "上下文详情")
	for _, tc := range []struct{ status, want string }{{"", "尚未压缩"}, {"compressed", "已生成摘要"}, {"fallback", "已回退裁剪"}, {"failed", "压缩失败"}} {
		f.state.Settings.Voice.ContextWindow = 65536
		// Provider counters must not replace the zero local estimate.
		f.state.Voice.Context = brain.ContextStats{LastCompression: tc.status, UsageAvailable: true, PromptTokens: 9000, CompletionTokens: 1000, TotalTokens: 10000}
		a.refresh()
		tt.Frame()
		for _, text := range []string{"等待模型初始化", "容量 64K", tc.want, "输入 0 · 输出 0 · 总计 0 Tokens（本地估算）"} {
			if !nativeContextHasText(tt.Texts(), text) {
				t.Fatalf("incorrect pending statistics, missing %q: %v", text, tt.Texts())
			}
		}
		if nativeContextHasText(tt.Texts(), "0.0%") {
			t.Fatalf("uninitialized context occupancy rendered as zero: %v", tt.Texts())
		}
		if nativeContextHasText(tt.Texts(), "总计 10,000") || nativeContextHasText(tt.Texts(), "服务商尚未上报") {
			t.Fatalf("provider usage leaked into local statistics: %v", tt.Texts())
		}
	}
	summary := nativeContextSummary(brain.ContextStats{SummaryTokens: 300})
	if !strings.Contains(summary, "已有摘要") {
		t.Fatalf("restored summary reported as uncompressed: %s", summary)
	}
}

func TestNativeContextDisplaysRestoredEstimateBeforeFirstReply(t *testing.T) {
	for _, tc := range []struct {
		name       string
		age        time.Duration
		localUsage bool
	}{
		{name: "saved_usage", localUsage: true},
		{name: "expired_history_keeps_usage", age: 5 * time.Hour, localUsage: true},
		{name: "legacy_history_without_usage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "voice_history.json")
			history := map[string]any{
				"version": 2, "updated_at": time.Now().Add(-tc.age), "summary": "用户喜欢清淡口味",
				"turns":        []map[string]string{{"user": "推荐晚饭", "assistant": "可以喝粥"}},
				"compressions": 2, "dropped_turns": 3, "last_compression": "compressed",
			}
			if tc.localUsage {
				history["local_usage"] = map[string]int{"input_tokens": 12000, "output_tokens": 700}
			}
			data, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			a, f, _, tt := nativeTest(t)
			// Startup configuration statistics must restore disk usage without a voice event or request.
			stats := brain.OpenMemory(path).ContextStatsForConfig(brain.Config{Model: "model", ContextWindow: 32768})
			f.state.Voice.Context = stats
			a.refresh()
			nativeClick(t, tt, "语音助手")
			nativeClick(t, tt, "上下文详情")
			usage := "输入 0 · 输出 0 · 总计 0 Tokens（本地估算）"
			if tc.localUsage {
				usage = "输入 12,000 · 输出 700 · 总计 12,700 Tokens（本地估算）"
				if stats.LocalPromptTokens != 12000 || stats.LocalCompletionTokens != 700 || stats.LocalTotalTokens != 12700 {
					t.Fatalf("startup configuration statistics lost saved local usage: %+v", stats)
				}
			}
			want := []string{"Tokens（估算）", "容量 32K", "32K · 手动设置", usage}
			if tc.age > 0 {
				want = append(want, "尚未压缩", "0 轮 · 压缩 0 次 · 丢弃 0 轮")
				if stats.SummaryTokens != 0 || stats.RetainedTurns != 0 {
					t.Fatalf("expired context was restored alongside saved usage: %+v", stats)
				}
			} else {
				want = append(want, "已生成摘要", "1 轮 · 压缩 2 次 · 丢弃 3 轮")
			}
			for _, text := range want {
				if !nativeContextHasText(tt.Texts(), text) {
					t.Fatalf("missing restored context display %q: %v", text, tt.Texts())
				}
			}
			if nativeContextHasText(tt.Texts(), "等待初始化") || nativeContextHasText(tt.Texts(), "等待模型初始化") {
				t.Fatalf("restored context still appears uninitialized: %v", tt.Texts())
			}
		})
	}
}

func TestNativeContextCapacitySources(t *testing.T) {
	model := strings.Repeat("provider/long-exact-model-id-", 8)
	for _, tc := range []struct{ source, mode, want string }{
		{"manual", "manual", "手动设置"},
		{"modelsdev", "auto", "models.dev"},
		{"fallback", "auto", "未获取，使用备用值"},
		{"", "", "未获取，使用备用值"}, {"", "manual", "手动设置"},
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
	nativeClick(t, tt, "上下文详情")
	for _, source := range []string{"manual", "fallback", "modelsdev"} {
		f.state.Voice.Context = brain.ContextStats{ContextWindowSource: source, ContextWindowModel: model, ContextWindow: 4096, InputBudget: 2253, EstimatedTokens: 1000}
		a.refresh()
		tt.Frame()
		want, _ := nativeContextSourceText(f.state.Voice.Context, "auto")
		if !nativeContextHasText(tt.Texts(), "4K · "+strings.ReplaceAll(want, "未获取，使用备用值", "使用备用值")) || nativeContextHasText(tt.Texts(), model) {
			t.Fatalf("missing source or crowded model ID: %v", tt.Texts())
		}
		for _, label := range []string{"上下文与用量", "上下文统计详情", "语音操作栏", "手动唤醒", "取消对话", "对话记录"} {
			r, ok := tt.Find(label)
			if !ok || r.W <= 0 || r.X < 206 || r.X+r.W > 760 || r.Y < 0 || r.Y+r.H > 600 {
				t.Fatalf("source %s escaped compact window: %s %+v", source, label, r)
			}
		}
	}
}

func TestNativeModelsDevTooltip(t *testing.T) {
	source, tooltip := nativeContextSourceText(brain.ContextStats{ContextWindowSource: "modelsdev", ContextWindowModel: "private-model", ContextWindowReference: "openai/exact-model", ContextInputLimit: 65536}, "auto")
	if source != "models.dev" {
		t.Fatal("catalog is mislabeled as provider metadata")
	}
	for _, text := range []string{"本地缓存", "不是当前服务的实测上限", "openai/exact-model", "private-model", "65,536"} {
		if !strings.Contains(tooltip, text) {
			t.Fatalf("catalog tooltip missing %q: %s", text, tooltip)
		}
	}
}

func TestNativeModelsDevSettingsSaveAndManualMode(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(1000, 1000)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "语音")
	nativeClick(t, tt, "对话服务")
	if a.draft.Voice.ModelsDevEnabled {
		t.Fatal("models.dev should be opt-in")
	}
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "使用 models.dev 查询上下文容量")
	nativeVoiceEdit(t, tt, "models.dev 服务商 ID", "openai")
	nativeVoiceEdit(t, tt, "models.dev 模型 ID", "exact-model")
	tt.Key(0, ui.KeyTab)
	a.refresh()
	tt.Frame()
	if !a.draft.Voice.ModelsDevEnabled || a.draft.Voice.ModelsDevProvider != "openai" || a.draft.Voice.ModelsDevModel != "exact-model" {
		t.Fatal("refresh discarded catalog settings draft")
	}
	nativeClick(t, tt, "保存设置")
	nativeComplete(t, updates, tt)
	if len(f.saves) != 1 || !f.saves[0].Voice.ModelsDevEnabled || f.saves[0].Voice.ModelsDevProvider != "openai" || f.saves[0].Voice.ModelsDevModel != "exact-model" {
		t.Fatal("catalog settings were not saved")
	}
	nativeVoiceClickRole(t, tt, ui.RoleSwitch, "自动读取模型上下文容量")
	if _, ok := tt.AccessByRole(ui.RoleSwitch, "使用 models.dev 查询上下文容量"); ok {
		t.Fatal("manual mode still offers automatic catalog lookup")
	}
	if !a.draft.Voice.ModelsDevEnabled || a.draft.Voice.ModelsDevModel != "exact-model" {
		t.Fatal("manual mode discarded saved catalog mapping")
	}
}

func TestNativeContextDisclosureAndCompactLayout(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, f, _, tt := nativeTest(t)
		tt.SetSize(760, 600)
		tt.SetDark(dark)
		f.state.Voice.Context = brain.ContextStats{
			ContextWindow: 2097152, InputBudget: 1676698, EstimatedTokens: 1500000, UsagePercent: 89.5,
			LastCompression: "failed", SummaryTokens: 1200, RetainedTurns: 4, Compressions: 12, DroppedTurns: 8,
			LocalPromptTokens: 123456789, LocalCompletionTokens: 23456789, LocalTotalTokens: 146913578,
			UsageAvailable: true, PromptTokens: 9000, CompletionTokens: 1000, TotalTokens: 10000,
		}
		a.refresh()
		nativeClick(t, tt, "语音助手")
		collapsed, _ := tt.Find("语音操作栏")
		if !tt.HasText("压缩失败") || !tt.HasText("89.5%") {
			t.Fatal("critical context status missing from collapsed view")
		}
		nativeClick(t, tt, "上下文详情")
		button, ok := tt.AccessByRole(ui.RoleButton, "上下文详情")
		if !ok || !button.Expanded {
			t.Fatal("disclosure does not expose expanded state")
		}
		for _, label := range []string{"上下文与用量", "上下文统计详情", "累计用量", "手动唤醒", "取消对话", "对话记录"} {
			r, ok := tt.Find(label)
			if !ok || r.W <= 0 || r.X < 226 || r.X+r.W > 741 || r.Y < 20 || r.Y+r.H > 581 {
				t.Fatalf("expanded context escaped compact window (dark=%v): %s %+v", dark, label, r)
			}
		}
		for _, text := range []string{"输入 123,456,789", "输出 23,456,789", "总计 146,913,578 Tokens（本地估算）"} {
			if !nativeContextHasText(tt.Texts(), text) {
				t.Fatalf("local usage was truncated, missing %q: %v", text, tt.Texts())
			}
		}
		nativeClick(t, tt, "上下文详情")
		if _, ok := tt.Find("上下文统计详情"); ok {
			t.Fatal("details remain after collapsing")
		}
		if after, _ := tt.Find("语音操作栏"); after.H != collapsed.H {
			t.Fatalf("collapse did not restore compact height: %v / %v", collapsed, after)
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
