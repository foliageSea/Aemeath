package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRestoredContextEstimateMatchesEngineWithoutRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("restoring statistics must not make network requests")
	}))
	defer server.Close()
	for _, tc := range []struct {
		name    string
		persona string
		tools   bool
		auto    bool
	}{
		{name: "manual default persona"},
		{name: "auto custom persona", persona: "  测试助手  ", auto: true},
		{name: "manual tools", tools: true},
		{name: "auto tools", tools: true, auto: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.json")
			data, err := json.Marshal(memoryFile{
				Version: memoryVersion, UpdatedAt: time.Now(),
				Turns:   []memoryTurn{{User: "我喜欢清淡的食物", Assistant: "记住了"}, {User: "推荐晚饭", Assistant: "可以喝粥"}},
				Summary: "用户不吃辣", Compressions: 2, DroppedTurns: 3, LastCompression: "compressed",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			memory := OpenMemory(path)
			cfg := Config{BaseURL: server.URL, Model: "exact-model", Persona: tc.persona, Tools: tc.tools, AutoContextWindow: tc.auto, ContextWindow: 65536, Memory: memory}
			before := memory.ContextStats()
			preview := memory.ContextStatsForConfig(cfg)
			if preview.EstimatedTokens <= 0 || preview.InputBudget <= 0 || preview.UsagePercent <= 0 || preview.RetainedTurns != 2 || preview.SummaryTokens <= 0 || preview.Compressions != 2 || preview.DroppedTurns != 3 || preview.LastCompression != "compressed" {
				t.Fatalf("incomplete restored statistics: %+v", preview)
			}
			if memory.ContextStats() != before || memory.revision != 0 {
				t.Fatal("preview changed shared memory or invalidated an Engine")
			}
			engine, err := New(context.Background(), cfg, &largeMetricsHost{})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if got := engine.ContextStats(); got != preview {
				t.Fatalf("preview differs from Engine:\npreview %+v\nengine  %+v", preview, got)
			}
			// Once an Engine publishes, its dynamic tool and capacity data win.
			engine.mu.Lock()
			engine.stats.ContextWindowSource = "provider"
			engine.contextPrompt = "动态设备说明"
			engine.toolTokens += 100
			engine.updateContextLocked()
			engine.mu.Unlock()
			if got := memory.ContextStatsForConfig(cfg); got != engine.ContextStats() {
				t.Fatalf("preview overwrote live Engine statistics: %+v", got)
			}
		})
	}
}

func TestConversationStatisticsPersistButUsageDoesNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	memory := OpenMemory(path)
	memory.turns = []memoryTurn{{User: "问题", Assistant: "回答"}}
	memory.updated, memory.summary = time.Now(), "摘要"
	memory.stats = ContextStats{
		Compressions: 7, DroppedTurns: 5, LastCompression: "fallback", Compressing: true,
		PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, UsageAvailable: true,
	}
	memory.persist()
	restored := OpenMemory(path)
	stats := restored.ContextStats()
	if stats.Compressions != 7 || stats.DroppedTurns != 5 || stats.LastCompression != "fallback" || stats.RetainedTurns != 1 || stats.SummaryTokens == 0 {
		t.Fatalf("conversation statistics not restored: %+v", stats)
	}
	if stats.Compressing || stats.UsageAvailable || stats.PromptTokens != 0 || stats.CompletionTokens != 0 || stats.TotalTokens != 0 {
		t.Fatalf("restored transient job or process usage: %+v", stats)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("memory permissions: %v, %v", info, err)
	}
	restored.Clear()
	cleared := restored.ContextStatsForConfig(Config{Model: "model"})
	if cleared.RetainedTurns != 0 || cleared.SummaryTokens != 0 || cleared.Compressions != 0 || cleared.DroppedTurns != 0 || cleared.LastCompression != "" {
		t.Fatalf("clear retained conversation statistics: %+v", cleared)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("preview recreated cleared memory file: %v", err)
	}
}

func TestRestoredContextEstimateExpiryAndConfiguration(t *testing.T) {
	memory := OpenMemory("")
	memory.turns = []memoryTurn{{User: "旧问题", Assistant: "旧回答"}}
	memory.updated, memory.summary = time.Now().Add(-MemoryTTL-time.Minute), "旧摘要"
	memory.stats = ContextStats{Compressions: 2, DroppedTurns: 4, LastCompression: "failed"}
	stats := memory.ContextStatsForConfig(Config{Model: "model"})
	if stats.InputBudget <= 0 || stats.EstimatedTokens != 0 || stats.RetainedTurns != 0 || stats.SummaryTokens != 0 || stats.UsagePercent != 0 || stats.Compressions != 0 || stats.DroppedTurns != 0 || stats.LastCompression != "" {
		t.Fatalf("expired context exposed historical statistics: %+v", stats)
	}
	memory.updated = time.Now()
	if got := memory.ContextStatsForConfig(Config{}); got.InputBudget != 0 {
		t.Fatalf("unconfigured model appears initialized: %+v", got)
	}
	if got := memory.ContextStatsForConfig(Config{Model: "model", ContextWindow: -1}); got.InputBudget != 0 {
		t.Fatalf("invalid capacity appears initialized: %+v", got)
	}
	first := memory.ContextStatsForConfig(Config{Model: "old-model", ContextWindow: 32768})
	second := memory.ContextStatsForConfig(Config{Model: "new-model", ContextWindow: 65536, Persona: "新角色设定", AutoContextWindow: true})
	if first.ContextWindow == second.ContextWindow || second.ContextWindowModel != "new-model" || second.ContextWindowSource != "detecting" || first.EstimatedTokens == second.EstimatedTokens {
		t.Fatalf("preview retained old model configuration: %+v / %+v", first, second)
	}
	if !reflect.DeepEqual(memory.Turns(), []MemoryTurn{{User: "旧问题", Assistant: "旧回答"}}) {
		t.Fatal("preview changed restored conversation")
	}
}
