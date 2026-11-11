package brain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestContextCapacityNeverQueriesConversationService(t *testing.T) {
	for _, auto := range []bool{false, true} {
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
				t.Errorf("unexpected capacity or metadata request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			posts.Add(1)
			readRequest(t, r)
			replyJSON(w, "回答")
		}))
		e, err := New(context.Background(), Config{BaseURL: server.URL + "/v1", APIKey: "private-key", Model: "org/model", Persona: "测试助手", ContextWindow: 65536, AutoContextWindow: auto, Memory: OpenMemory("")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := e.Reply(context.Background(), "问题"); err != nil {
				t.Fatal(err)
			}
		}
		wantSource := "manual"
		if auto {
			wantSource = "fallback"
		}
		if stats := e.ContextStats(); stats.ContextWindow != 65536 || stats.ContextWindowSource != wantSource || posts.Load() != 2 {
			t.Fatalf("incorrect capacity or requests: %+v POST=%d", stats, posts.Load())
		}
		e.Close()
		server.Close()
	}
}

func TestCatalogCapacityAppliesBeforeCompressionAndSurvivesReset(t *testing.T) {
	var summaries, normal atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected provider metadata request: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		req := readRequest(t, r)
		if isSummaryRequest(req) {
			summaries.Add(1)
			replyJSON(w, "用户需要简短回答。")
			return
		}
		normal.Add(1)
		replyJSON(w, "回答")
	}))
	defer server.Close()
	catalog := testModelCatalog(t)
	provider := catalog.providers["test"]
	entry := provider.Models["org/model"]
	entry.Limit.Context, entry.Limit.Input = 8192, 4096
	provider.Models["org/model"] = entry
	e, err := New(context.Background(), Config{BaseURL: server.URL, Model: "org/model", Persona: "测试助手", AutoContextWindow: true, ModelsDev: catalog, ModelsDevProvider: "test", Memory: OpenMemory("")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	seedContext(e, 12)
	if _, err := e.Reply(context.Background(), "继续"); err != nil {
		t.Fatal(err)
	}
	stats := e.ContextStats()
	if summaries.Load() != 1 || normal.Load() != 1 || stats.ContextWindow != 8192 || stats.InputBudget != 4096 || stats.ContextWindowSource != "modelsdev" || stats.ContextWindowReference != "test/org/model" {
		t.Fatalf("catalog capacity not enforced before generation: %+v summaries=%d", stats, summaries.Load())
	}
	e.Clear()
	if got := e.ContextStats(); got.InputBudget != stats.InputBudget || got.ContextWindowReference != stats.ContextWindowReference {
		t.Fatal("clear erased catalog input restriction")
	}
	e.mu.Lock()
	e.lastActive = time.Now().Add(-MemoryTTL - time.Minute)
	e.mu.Unlock()
	if _, err := e.Reply(context.Background(), "新问题"); err != nil {
		t.Fatal(err)
	}
	if got := e.ContextStats(); got.ContextWindow != 8192 || got.InputBudget != 4096 || got.ContextWindowSource != "modelsdev" {
		t.Fatalf("TTL reset erased catalog capacity: %+v", got)
	}
}

func TestContextCapacityRejectsStaleOrCanceledWork(t *testing.T) {
	for _, mode := range []string{"cancel", "clear", "external_clear", "replacement", "close"} {
		t.Run(mode, func(t *testing.T) {
			catalog := testModelCatalog(t)
			cfg := Config{BaseURL: "https://provider.example/v1", Model: "org/model", Persona: "测试助手", AutoContextWindow: true, ModelsDev: catalog, Memory: OpenMemory("")}
			e, err := New(context.Background(), cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			generation := e.generation
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "cancel":
				cancel()
			case "clear":
				e.Clear()
			case "external_clear":
				cfg.Memory.Clear()
			case "replacement":
				next, err := New(context.Background(), cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer next.Close()
			case "close":
				e.Close()
			}
			before := cfg.Memory.ContextStats()
			if err := e.resolveContextWindow(ctx, generation); !errors.Is(err, context.Canceled) {
				t.Fatalf("stale capacity update accepted: %v", err)
			}
			if got := cfg.Memory.ContextStats(); got != before {
				t.Fatalf("stale capacity update changed shared statistics: %+v", got)
			}
		})
	}
}

func TestChatRouteStillPreservesEscapedServicePrefix(t *testing.T) {
	for _, tc := range []struct{ base, prefix string }{
		{"/tenant%2F", "/tenant%2F"},
		{"/tenant%2Fid/v1/chat/completions", "/tenant%2Fid/v1"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.RequestURI != tc.prefix+"/chat/completions" {
					t.Errorf("wrong chat route or unexpected metadata request: %s %s", r.Method, r.RequestURI)
				}
				readRequest(t, r)
				replyJSON(w, "回答")
			}))
			defer server.Close()
			e, err := New(context.Background(), Config{BaseURL: server.URL + tc.base, Model: "org/model", Persona: "测试助手", AutoContextWindow: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if _, err := e.Reply(context.Background(), "问题"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
