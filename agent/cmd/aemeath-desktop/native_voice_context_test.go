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

func TestNativeContextPercentageSharesFooterWithStatusWithoutProgress(t *testing.T) {
	for _, size := range [][2]int{{760, 577}, {1100, 900}} {
		for _, dark := range []bool{false, true} {
			a, f, _, tt := nativeTest(t)
			tt.SetSize(size[0], size[1])
			tt.SetDark(dark)
			f.state.Voice.Context = brain.ContextStats{ContextWindow: 1050000, InputBudget: 838976, EstimatedTokens: 5414, UsagePercent: 0.6, ContextWindowSource: "modelsdev"}
			a.refresh()
			nativeClick(t, tt, "语音助手")
			for _, expanded := range []bool{false, true} {
				if expanded {
					nativeClick(t, tt, "上下文详情")
				}
				if _, ok := tt.Find("上下文占用（估算）"); ok {
					t.Fatal("obsolete context progress bar remains")
				}
				context, ok := tt.Find("上下文")
				if !ok {
					t.Fatal("missing context footer text")
				}
				percent, ok := tt.Find("0.6%")
				if !ok || percent.X < context.X+context.W || percent.Y > context.Y+context.H || percent.Y+percent.H < context.Y || percent.X+percent.W > float32(size[0]-20) {
					t.Fatalf("percentage must follow context on same row: context=%+v percent=%+v", context, percent)
				}
				status, ok := tt.Find("当前对话状态")
				if !ok || status.Y > percent.Y+percent.H || status.Y+status.H < percent.Y || status.X+status.W > context.X {
					t.Fatalf("activity and context must share footer without overlap: status=%+v context=%+v percent=%+v", status, context, percent)
				}
				if tt.HasText("容量 1,050,000") {
					t.Fatal("redundant summary capacity remains")
				}
				if expanded && !tt.HasText("1,050,000 · models.dev") {
					t.Fatal("expanded capacity detail was removed")
				}
			}
		}
	}
}

func TestNativeContextStatsRefreshWithoutVoiceEvent(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	f.state.Voice.Phase = "listening"
	f.state.Voice.Context = brain.ContextStats{ContextWindowSource: "modelsdev", ContextWindowModel: strings.Repeat("very-long-request-model-id-", 8), EstimatedTokens: 21000, InputBudget: 28000, ContextWindow: 32768, UsagePercent: 75, Compressing: true, RetainedTurns: 4}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{"75.0%", "正在压缩"} {
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
		for _, text := range []string{"等待模型初始化", "64K · 使用备用值", tc.want, "输入 0 · 输出 0 · 总计 0 Tokens（本地估算）"} {
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
		{name: "five_hour_history_and_summary_with_usage", age: 5 * time.Hour, localUsage: true},
		{name: "multi_day_history_and_summary_with_usage", age: 7 * 24 * time.Hour, localUsage: true},
		{name: "legacy_history_without_usage"},
		{name: "five_hour_history_and_summary_without_usage", age: 5 * time.Hour},
		{name: "multi_day_history_and_summary_without_usage", age: 7 * 24 * time.Hour},
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
			memory := brain.OpenMemory(path)
			turns := memory.Turns()
			if len(turns) != 1 || turns[0].User != "推荐晚饭" || turns[0].Assistant != "可以喝粥" {
				t.Fatalf("idle age %v discarded restored conversation text: %+v", tc.age, turns)
			}
			stats := memory.ContextStatsForConfig(brain.Config{Model: "model", ContextWindow: 32768})
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
			} else if stats.LocalPromptTokens != 0 || stats.LocalCompletionTokens != 0 || stats.LocalTotalTokens != 0 {
				t.Fatalf("missing saved local usage must restore as zero: %+v", stats)
			}
			if stats.SummaryTokens <= 0 || stats.RetainedTurns != 1 || stats.Compressions != 2 || stats.DroppedTurns != 3 || stats.LastCompression != "compressed" {
				t.Fatalf("idle age %v discarded restored history, summary or compression metadata: %+v", tc.age, stats)
			}
			want := []string{"Tokens（估算）", "32K · 手动设置", usage, "已生成摘要", "1 轮 · 压缩 2 次 · 丢弃 3 轮"}
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
	f.state.Voice.Phase = "listening"
	a.refresh()
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
	nativeClick(t, tt, "助手")
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
		f.state.Voice.Phase = "listening"
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

func TestNativeContextDetailsContainLiveRoundAndPlayback(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	tt.SetSize(760, 600)
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.Phase = "speaking"
	f.state.Voice.Turn = 3
	f.state.Voice.GeneratedSegments = 4
	f.state.Voice.PlayedSegments = 1
	a.refresh()
	nativeClick(t, tt, "语音助手")
	live := "第 3 轮 · 已播放 1 段 · 已合成 4 段"
	if tt.HasText(live) {
		t.Fatal("collapsed composer exposes live round details")
	}
	if !tt.HasText("正在播放回复 · 已播 1/4 段") {
		t.Fatal("collapsed footer lost current playback status")
	}
	nativeClick(t, tt, "上下文详情")
	details, ok := tt.Find("上下文统计详情")
	line, found := tt.Find(live)
	if !ok || !found || line.X < details.X || line.X+line.W > details.X+details.W || line.Y < details.Y || line.Y+line.H > details.Y+details.H {
		t.Fatalf("live details must be inside expanded panel: panel=%+v live=%+v", details, line)
	}
	f.state.Voice.PlayedSegments = 2
	a.refresh()
	tt.Frame()
	if !tt.HasText("第 3 轮 · 已播放 2 段 · 已合成 4 段") || tt.HasText(live) {
		t.Fatal("expanded playback details did not refresh")
	}
	button, ok := tt.AccessByRole(ui.RoleButton, "上下文详情")
	if !ok || !button.Expanded {
		t.Fatal("playback refresh collapsed context details")
	}
	nativeClick(t, tt, "上下文详情")
	if tt.HasText("第 3 轮 · 已播放 2 段 · 已合成 4 段") {
		t.Fatal("live details remain after collapsing")
	}
}

func TestNativeContextWindowSettingsSurviveRefreshAndSave(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	tt.SetSize(760, 600)
	nativeClick(t, tt, "设置")
	nativeClick(t, tt, "助手")
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
