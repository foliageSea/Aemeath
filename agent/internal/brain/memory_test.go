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

func newMemoryEngine(t *testing.T, s *httptest.Server, m *Memory) *Engine {
	t.Helper()
	e, err := New(context.Background(), Config{BaseURL: s.URL + "/v1", APIKey: "k", Model: "mock-model", Persona: "测试助手", Memory: m}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMemorySurvivesEngineAndRestart(t *testing.T) {
	var seen []int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, len(readRequest(t, r)["messages"].([]any)))
		replyJSON(w, "好的")
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "voice_history.json")

	first := newMemoryEngine(t, s, OpenMemory(path))
	if _, err := first.Reply(context.Background(), "我叫小明"); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("memory file %v %v", info, err)
	}

	// A fresh Memory simulates an app restart; a fresh Engine a reconnect.
	second := newMemoryEngine(t, s, OpenMemory(path))
	if _, err := second.Reply(context.Background(), "我叫什么"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 2 || seen[1] != 4 {
		t.Fatalf("message counts=%v; restored history not sent", seen)
	}

	second.Clear()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Clear kept memory file: %v", err)
	}
	if h, _ := OpenMemory(path).snapshot(); len(h) != 0 {
		t.Fatal("cleared memory reloaded")
	}
}

func TestMemoryExpiresAfterInactivity(t *testing.T) {
	var counts []int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts = append(counts, len(readRequest(t, r)["messages"].([]any)))
		replyJSON(w, "好的")
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "voice_history.json")
	b, _ := json.Marshal(memoryFile{Version: memoryVersion, UpdatedAt: time.Now().Add(-MemoryTTL - time.Minute), Turns: []memoryTurn{{User: "旧问题", Assistant: "旧回答"}}})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	e := newMemoryEngine(t, s, OpenMemory(path))
	if _, err := e.Reply(context.Background(), "新问题"); err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0] != 2 {
		t.Fatalf("expired history sent: %v", counts)
	}
	if h, _ := OpenMemory(path).snapshot(); len(h) != 2 || h[0].Content != "新问题" {
		t.Fatalf("persisted=%v", h)
	}
}

func TestMalformedMemoryStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice_history.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h, _ := OpenMemory(path).snapshot(); len(h) != 0 {
		t.Fatal("malformed memory loaded")
	}
}
