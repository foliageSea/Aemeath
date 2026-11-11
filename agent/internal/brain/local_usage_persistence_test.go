package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalUsagePersistsAcrossRestartAndContinues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		replyJSON(w, "已记录")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "voice_history.json")
	first := contextEngine(t, server, OpenMemory(path), false, nil)
	if _, err := first.Reply(context.Background(), "第一问"); err != nil {
		t.Fatal(err)
	}
	before := first.ContextStats()
	if before.LocalPromptTokens <= 0 || before.LocalCompletionTokens <= 0 {
		t.Fatalf("usage not recorded: %+v", before)
	}
	restored := OpenMemory(path)
	if got := restored.ContextStats(); got.LocalPromptTokens != before.LocalPromptTokens || got.LocalCompletionTokens != before.LocalCompletionTokens || got.LocalTotalTokens != before.LocalTotalTokens {
		t.Fatalf("usage not on disk before shutdown: %+v, want %+v", got, before)
	}
	first.Close()
	second := contextEngine(t, server, restored, false, nil)
	if got := second.ContextStats(); got.LocalTotalTokens != before.LocalTotalTokens {
		t.Fatalf("startup reset cumulative usage: %+v", got)
	}
	if _, err := second.Reply(context.Background(), "第二问"); err != nil {
		t.Fatal(err)
	}
	after := second.ContextStats()
	if after.LocalPromptTokens <= before.LocalPromptTokens || after.LocalCompletionTokens != 2*before.LocalCompletionTokens {
		t.Fatalf("usage did not continue: before=%+v after=%+v", before, after)
	}
	if got := OpenMemory(path).ContextStats(); got.LocalTotalTokens != after.LocalTotalTokens {
		t.Fatalf("new totals not persisted: %+v", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file permissions: %v %v", info, err)
	}
}

func TestLocalUsagePersistsIncompleteTurnsAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice_history.json")
	memory := OpenMemory(path)
	engine := &Engine{memory: memory}
	scope := requestScope{}
	engine.addLocalUsage(100, 0, scope)
	if got := OpenMemory(path).ContextStats(); got.LocalPromptTokens != 100 || got.LocalTotalTokens != 100 {
		t.Fatalf("input waiting for transcript commit: %+v", got)
	}
	engine.recordUsage([]byte(`{"choices":[{"message":{"content":"收到"}}]}`), scope)
	restored := OpenMemory(path)
	if len(restored.Turns()) != 0 || restored.ContextStats().LocalCompletionTokens != 4 || restored.ContextStats().LocalTotalTokens != 104 {
		t.Fatalf("incomplete turn output not persisted: %+v", restored.ContextStats())
	}
	engine.Clear()
	engine.addLocalUsage(5, 5, scope)
	if got := OpenMemory(path).ContextStats(); got.LocalTotalTokens != 0 {
		t.Fatalf("stale response restored cleared totals: %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clear did not remove saved usage: %v", err)
	}
}

func TestLocalUsageSurvivesExpiredConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice_history.json")
	memory := OpenMemory(path)
	memory.updated = time.Now().Add(-MemoryTTL - time.Minute)
	memory.turns = []memoryTurn{{User: "旧问题", Assistant: "旧回答"}}
	memory.stats.LocalPromptTokens, memory.stats.LocalCompletionTokens, memory.stats.LocalTotalTokens = 100, 10, 110
	memory.persist()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if n := len(req["messages"].([]any)); n != 2 {
			t.Errorf("expired context sent %d messages", n)
		}
		replyJSON(w, "新回答")
	}))
	defer server.Close()
	restored := OpenMemory(path)
	if got := restored.ContextStats(); got.RetainedTurns != 0 || got.LocalTotalTokens != 110 {
		t.Fatalf("expiry hid cumulative usage: %+v", got)
	}
	engine := contextEngine(t, server, restored, false, nil)
	stale := requestScope{generation: engine.generation, epoch: engine.memoryEpoch}
	if _, err := engine.Reply(context.Background(), "新问题"); err != nil {
		t.Fatal(err)
	}
	after := engine.ContextStats()
	if after.LocalPromptTokens <= 100 || after.LocalCompletionTokens != 16 {
		t.Fatalf("expiry discarded accumulated usage: %+v", after)
	}
	engine.addLocalUsage(20, 20, stale)
	if got := OpenMemory(path).ContextStats(); got.LocalTotalTokens != after.LocalTotalTokens {
		t.Fatalf("late expired response changed persisted totals: %+v", got)
	}
	engine.Clear()
	if got := OpenMemory(path).ContextStats(); got.LocalTotalTokens != 0 {
		t.Fatalf("manual clear retained saved usage: %+v", got)
	}
}

func TestLocalUsageRestoresLegacyAndInvalidCounters(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, tc := range []struct {
			name  string
			usage *localUsageFile
			want  int
		}{
			{name: "legacy"},
			{name: "valid", usage: &localUsageFile{InputTokens: 123, OutputTokens: 45}, want: 168},
			{name: "negative", usage: &localUsageFile{InputTokens: -1, OutputTokens: 45}},
			{name: "overflow", usage: &localUsageFile{InputTokens: int(^uint(0) >> 1), OutputTokens: 1}},
		} {
			t.Run(tc.name+string(rune('0'+version)), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "voice_history.json")
				data, err := json.Marshal(memoryFile{Version: version, UpdatedAt: time.Now(), Turns: []memoryTurn{{User: "问题", Assistant: "回答"}}, LocalUsage: tc.usage})
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				restored := OpenMemory(path)
				if got := restored.ContextStats(); got.LocalTotalTokens != tc.want || got.RetainedTurns != 1 || got.UsageAvailable {
					t.Fatalf("invalid restoration: %+v", got)
				}
			})
		}
	}
}
