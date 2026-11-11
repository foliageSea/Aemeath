package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelContextMetadataParsing(t *testing.T) {
	for _, tc := range []struct {
		name, body, model string
		want              int
	}{
		{"detail", `{"id":"org/model","context_length":131072}`, "org/model", 131072},
		{"provider lower limit", `{"data":{"id":"org/model","context_length":131072,"top_provider":{"context_length":65536,"max_completion_tokens":8192}}}`, "org/model", 65536},
		{"exact list match", `{"data":[{"id":"other","context_length":1048576},{"id":"org/model","max_model_len":32768}]}`, "org/model", 32768},
		{"duplicate limits choose smaller", `{"data":[{"id":"x","context_length":65536},{"id":"x","context_window":8192}]}`, "x", 8192},
		{"numeric string", `{"id":"x","max_context_length":"16384"}`, "x", 16384},
		{"small provider window", `{"id":"x","context_window":4096}`, "x", 4096},
		{"wrong model", `{"id":"x-latest","context_length":131072}`, "x", 0},
		{"missing identity", `{"context_length":131072}`, "x", 0},
		{"output and consumption are not capacity", `{"id":"x","max_tokens":131072,"max_completion_tokens":8192,"usage":{"prompt_tokens":65536}}`, "x", 0},
		{"unknown nested capacity", `{"id":"x","pricing":{"context_length":131072}}`, "x", 0},
		{"ambiguous units", `{"id":"x","context_length":"128K"}`, "x", 0},
		{"fraction", `{"id":"x","context_length":8192.5}`, "x", 0},
		{"negative", `{"id":"x","context_length":-1}`, "x", 0},
		{"too small", `{"id":"x","context_length":100}`, "x", 0},
		{"too large", `{"id":"x","context_length":999999999}`, "x", 0},
		{"trailing json", `{"id":"x","context_length":65536} {}`, "x", 0},
		{"standard model no extensions", `{"id":"x","object":"model","created":1,"owned_by":"provider"}`, "x", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelContextFromJSON([]byte(tc.body), tc.model); got != tc.want {
				t.Fatalf("context=%d want=%d", got, tc.want)
			}
		})
	}
}

func newAutoContextEngine(t *testing.T, s *httptest.Server, model string, fallback int, auto bool) *Engine {
	t.Helper()
	e, err := New(context.Background(), Config{BaseURL: s.URL + "/v1", APIKey: "metadata-key", Model: model, Persona: "测试助手", ContextWindow: fallback, AutoContextWindow: auto, Memory: OpenMemory("")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestModelContextAutoCapacityAppliesBeforeFirstGeneration(t *testing.T) {
	var lookups, summaries, normal atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			lookups.Add(1)
			if r.Header.Get("Authorization") != "Bearer metadata-key" || r.URL.EscapedPath() != "/v1/models/org%2Fmodel" || r.URL.RawQuery != "" {
				t.Errorf("wrong metadata request %s", r.URL.EscapedPath())
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "org/model", "context_length": 8192})
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
	defer s.Close()
	e := newAutoContextEngine(t, s, "org/model", 32768, true)
	seedContext(e, 12)
	if e.ContextStats().ContextWindowSource != "detecting" {
		t.Fatal("missing pending source")
	}
	if _, err := e.Reply(context.Background(), "继续"); err != nil {
		t.Fatal(err)
	}
	stats := e.ContextStats()
	if lookups.Load() != 1 || summaries.Load() != 1 || normal.Load() != 1 || stats.ContextWindow != 8192 || stats.InputBudget != newContextStats(8192).InputBudget || stats.ContextWindowSource != "provider" || stats.ContextWindowModel != "org/model" {
		t.Fatalf("capacity not applied before generation: %+v metadata=%d summaries=%d", stats, lookups.Load(), summaries.Load())
	}
	if e.memory.ContextStats().ContextWindowSource != "provider" {
		t.Fatal("source not shared with desktop memory")
	}
	e.Clear()
	if e.ContextStats().ContextWindowSource != "provider" || e.ContextStats().ContextWindow != 8192 {
		t.Fatal("clear erased capacity")
	}
	if _, err := e.Reply(context.Background(), "新问题"); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 {
		t.Fatal("metadata fetched for each question")
	}
	// Conversation TTL resets must retain the model's effective capacity/source.
	e.mu.Lock()
	e.lastActive = time.Now().Add(-MemoryTTL - time.Minute)
	e.mu.Unlock()
	if _, err := e.Reply(context.Background(), "过期后问题"); err != nil {
		t.Fatal(err)
	}
	if e.ContextStats().ContextWindowSource != "provider" || e.ContextStats().ContextWindow != 8192 {
		t.Fatal("TTL erased capacity")
	}
}

func TestModelContextFallbackAndManual(t *testing.T) {
	for _, mode := range []string{"missing", "wrong_model", "unavailable", "manual"} {
		t.Run(mode, func(t *testing.T) {
			var get, post atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					get.Add(1)
					if mode == "unavailable" {
						w.WriteHeader(503)
						return
					}
					id := "actual"
					if mode == "wrong_model" {
						id = "actual-latest"
					}
					response := map[string]any{"id": id, "object": "model"}
					if mode == "wrong_model" {
						response["context_length"] = 1048576
					}
					if r.URL.Path == "/v1/models" {
						json.NewEncoder(w).Encode(map[string]any{"data": []any{response}})
					} else {
						json.NewEncoder(w).Encode(response)
					}
					return
				}
				readRequest(t, r)
				post.Add(1)
				replyJSON(w, "回答")
			}))
			defer s.Close()
			e := newAutoContextEngine(t, s, "actual", 65536, mode != "manual")
			for i := 0; i < 2; i++ {
				if _, err := e.Reply(context.Background(), "问题"); err != nil {
					t.Fatal(err)
				}
			}
			wantSource, wantGet := "fallback", int32(2)
			if mode == "manual" {
				wantSource = "manual"
				wantGet = 0
			}
			stats := e.ContextStats()
			if stats.ContextWindow != 65536 || stats.ContextWindowSource != wantSource || get.Load() != wantGet || post.Load() != 2 {
				t.Fatalf("incorrect fallback: %+v GET=%d POST=%d", stats, get.Load(), post.Load())
			}
		})
	}
}

func TestModelContextListFallbackAndRedirectDoesNotForwardCredentials(t *testing.T) {
	var externalHits atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalHits.Add(1)
		t.Error("followed metadata redirect")
	}))
	defer external.Close()
	var metadata atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			readRequest(t, r)
			replyJSON(w, "答")
			return
		}
		metadata.Add(1)
		if r.URL.Path != "/v1/models" {
			http.Redirect(w, r, external.URL+"/models", http.StatusTemporaryRedirect)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "actual", "context_length": 131072, "top_provider": map[string]any{"context_length": 65536}}}})
	}))
	defer s.Close()
	e := newAutoContextEngine(t, s, "actual", 32768, true)
	if _, err := e.Reply(context.Background(), "问题"); err != nil {
		t.Fatal(err)
	}
	if e.ContextStats().ContextWindow != 65536 || externalHits.Load() != 0 || metadata.Load() != 2 {
		t.Fatal("redirect leaked or list not used")
	}
}

func TestModelContextCancellationAndClearDiscardPendingDiscovery(t *testing.T) {
	for _, mode := range []string{"cancel", "clear", "external_clear", "close"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var gets atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					readRequest(t, r)
					replyJSON(w, "回答")
					return
				}
				if gets.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "actual", "context_length": 131072})
			}))
			defer s.Close()
			e := newAutoContextEngine(t, s, "actual", 32768, true)
			seedContext(e, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := e.Reply(ctx, "旧问题"); done <- err }()
			<-entered
			switch mode {
			case "cancel":
				cancel()
			case "clear":
				e.Clear()
			case "external_clear":
				e.memory.Clear()
			case "close":
				e.Close()
			}
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("pending discovery err=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("discovery did not cancel")
			}
			wantTurns := 1
			if mode == "clear" || mode == "external_clear" {
				wantTurns = 0
			}
			if e.ContextStats().ContextWindow != 32768 || len(e.memory.Turns()) != wantTurns {
				t.Fatal("stale metadata or reply committed")
			}
			if mode != "close" {
				if _, err := e.Reply(context.Background(), "新问题"); err != nil {
					t.Fatal(err)
				}
				if e.ContextStats().ContextWindowSource != "provider" || e.ContextStats().ContextWindow != 131072 {
					t.Fatal("canceled discovery not retried")
				}
			}
		})
	}
}

func TestModelContextMetadataTimeoutStillAllowsChat(t *testing.T) {
	var gets, posts atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			<-r.Context().Done()
			return
		}
		readRequest(t, r)
		posts.Add(1)
		replyJSON(w, "回答")
	}))
	defer s.Close()
	e := newAutoContextEngine(t, s, "actual", 32768, true)
	start := time.Now()
	if _, err := e.Reply(context.Background(), "问题"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second || gets.Load() != 2 || posts.Load() != 1 || e.ContextStats().ContextWindowSource != "fallback" {
		t.Fatalf("metadata timeout blocked chat: %s %+v", time.Since(start), e.ContextStats())
	}
}

func TestModelContextEscapedServicePrefixMatchesChatRoute(t *testing.T) {
	for _, tc := range []struct{ base, prefix string }{
		{"/tenant%2F", "/tenant%2F"},
		{"/tenant%2Fid/v1/chat/completions", "/tenant%2Fid/v1"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			var gets, posts atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
					if r.RequestURI != tc.prefix+"/models/org%2Fmodel" {
						t.Errorf("metadata used wrong deployment route: %s", r.RequestURI)
					}
					json.NewEncoder(w).Encode(map[string]any{"id": "org/model", "context_length": 65536})
					return
				}
				posts.Add(1)
				if r.RequestURI != tc.prefix+"/chat/completions" {
					t.Errorf("chat used wrong deployment route: %s", r.RequestURI)
				}
				readRequest(t, r)
				replyJSON(w, "回答")
			}))
			defer s.Close()
			e, err := New(context.Background(), Config{BaseURL: s.URL + tc.base, Model: "org/model", Persona: "测试助手", AutoContextWindow: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if _, err := e.Reply(context.Background(), "问题"); err != nil {
				t.Fatal(err)
			}
			if gets.Load() != 1 || posts.Load() != 1 || e.ContextStats().ContextWindow != 65536 {
				t.Fatalf("wrong routes or capacity %+v", e.ContextStats())
			}
		})
	}
}

func TestModelContextOversizedMetadataIgnored(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			readRequest(t, r)
			replyJSON(w, "回答")
			return
		}
		prefix := `{"id":"actual","context_length":131072}`
		ioText := prefix + strings.Repeat(" ", modelMetadataLimit)
		w.Write([]byte(ioText))
	}))
	defer s.Close()
	e := newAutoContextEngine(t, s, "actual", 32768, true)
	if _, err := e.Reply(context.Background(), "问题"); err != nil {
		t.Fatal(err)
	}
	if stats := e.ContextStats(); stats.ContextWindow != 32768 || stats.ContextWindowSource != "fallback" {
		t.Fatalf("oversized metadata accepted %+v", stats)
	}
}
