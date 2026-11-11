package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testCatalogJSON = `{
	"test": {"api":"https://provider.example/v1","models":{
		"org/model":{"id":"org/model","limit":{"context":131072,"input":65536,"output":8192}},
		"output-only":{"id":"output-only","limit":{"output":65536}},
		"bad-id":{"id":"other","limit":{"context":65536}},
		"negative":{"id":"negative","limit":{"context":-1}},
		"too-large":{"id":"too-large","limit":{"context":999999999}},
		"bad-input":{"id":"bad-input","limit":{"context":65536,"input":-1}}
	}},
	"openai":{"models":{"exact":{"id":"exact","limit":{"context":65536}}}}
}`

func testModelCatalog(t *testing.T) *ModelCatalog {
	t.Helper()
	c := NewModelCatalog("")
	if err := json.Unmarshal([]byte(testCatalogJSON), &c.providers); err != nil {
		t.Fatal(err)
	}
	c.updated = time.Now()
	return c
}

func awaitCatalog(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog operation did not complete")
	}
}

func TestModelsCatalogExactMatching(t *testing.T) {
	c := testModelCatalog(t)
	for _, tc := range []struct {
		name, base, model, provider, mapped string
		window, input                       int
	}{
		{"exact", "https://provider.example/v1", "org/model", "", "", 131072, 65536},
		{"completion URL", "https://provider.example/v1/chat/completions/", "org/model", "", "", 131072, 65536},
		{"known native endpoint", "https://api.openai.com/v1", "exact", "", "", 65536, 0},
		{"explicit proxy mapping", "https://proxy.private/v1", "my-deployment", "test", "org/model", 131072, 65536},
		{"explicit provider exact model", "https://proxy.private/v1", "org/model", "test", "", 131072, 65536},
		{"proxy needs mapping", "https://proxy.private/v1", "org/model", "", "", 0, 0},
		{"model override needs provider", "https://provider.example/v1", "alias", "", "org/model", 0, 0},
		{"wrong provider", "https://provider.example/v1", "org/model", "unknown", "", 0, 0},
		{"suffix not removed", "https://provider.example/v1", "org/model-latest", "", "", 0, 0},
		{"case sensitive ID", "https://provider.example/v1", "org/Model", "", "", 0, 0},
		{"prefix not removed", "https://provider.example/v1", "vendor/org/model", "", "", 0, 0},
		{"host substring rejected", "https://provider.example.evil/v1", "org/model", "", "", 0, 0},
		{"different tenant", "https://provider.example/tenant/v1", "org/model", "", "", 0, 0},
		{"different scheme", "http://provider.example/v1", "org/model", "", "", 0, 0},
		{"query rejected", "https://provider.example/v1?token=x", "org/model", "", "", 0, 0},
		{"userinfo rejected", "https://user@provider.example/v1", "org/model", "", "", 0, 0},
		{"output not capacity", "https://provider.example/v1", "output-only", "", "", 0, 0},
		{"mismatched ID", "https://provider.example/v1", "bad-id", "", "", 0, 0},
		{"negative", "https://provider.example/v1", "negative", "", "", 0, 0},
		{"too large", "https://provider.example/v1", "too-large", "", "", 0, 0},
		{"invalid input", "https://provider.example/v1", "bad-input", "", "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, ok := c.lookup(tc.base, tc.model, tc.provider, tc.mapped)
			if ok != (tc.window > 0) || (ok && (match.window != tc.window || match.input != tc.input || match.reference == "")) {
				t.Fatalf("lookup=%+v ok=%v", match, ok)
			}
		})
	}
	c.providers["duplicate"] = c.providers["test"]
	if _, ok := c.lookup("https://provider.example/v1", "org/model", "", ""); ok {
		t.Fatal("ambiguous provider match accepted")
	}
}

func TestModelsCatalogDownloadCacheAndPrivacy(t *testing.T) {
	var hits atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
		}
		if r.Method != "GET" || r.RequestURI != "/api.json" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.ContentLength > 0 {
			t.Errorf("catalog request leaked configuration: %s, %v", r.RequestURI, r.Header)
		}
		<-release
		w.Write([]byte(testCatalogJSON))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "models_dev_cache.json")
	c := NewModelCatalog(path)
	c.endpoint = server.URL + "/api.json"
	done := c.RefreshAsync(context.Background())
	awaitCatalog(t, entered)
	if c.RefreshAsync(context.Background()) != done {
		t.Fatal("concurrent refresh was not coalesced")
	}
	if _, ok := c.lookup("https://provider.example/v1", "org/model", "", ""); ok {
		t.Fatal("unfetched catalog unexpectedly populated")
	}
	close(release)
	awaitCatalog(t, done)
	awaitCatalog(t, c.RefreshAsync(context.Background()))
	if hits.Load() != 1 {
		t.Fatal("fresh cache redownloaded")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions: %v %v", info, err)
	}
	restored := NewModelCatalog(path)
	if match, ok := restored.lookup("https://provider.example/v1", "org/model", "", ""); !ok || match.window != 131072 || match.input != 65536 {
		t.Fatalf("restart lost cached metadata: %+v %v", match, ok)
	}
	// A fresh disk cache avoids network after restart, too.
	restored.endpoint = server.URL + "/api.json"
	awaitCatalog(t, restored.RefreshAsync(context.Background()))
	if hits.Load() != 1 {
		t.Fatal("restart redownloaded fresh catalog")
	}
	stored, err := os.ReadFile(path)
	if err != nil || bytes.Contains(stored, []byte(`"output":`)) {
		t.Fatal("cache did not retain the minimal context-only schema")
	}
}

func TestModelsCatalogFailureRetainsStaleCache(t *testing.T) {
	for _, mode := range []string{"unavailable", "malformed", "trailing", "empty", "no-models", "oversized", "redirect", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var hits, externalHits atomic.Int32
			external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { externalHits.Add(1) }))
			defer external.Close()
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				close(entered)
				switch mode {
				case "unavailable":
					w.WriteHeader(503)
				case "malformed":
					w.Write([]byte(`{"test":`))
				case "trailing":
					w.Write([]byte(testCatalogJSON + `{}`))
				case "empty":
					w.Write([]byte(`{}`))
				case "no-models":
					w.Write([]byte(`{"error":{"message":"unavailable"}}`))
				case "oversized":
					w.Header().Set("Content-Length", strconv.Itoa(modelsCatalogMaxBytes+1))
					w.Write([]byte(testCatalogJSON))
				case "redirect":
					http.Redirect(w, r, external.URL, http.StatusFound)
				case "cancel":
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			c := testModelCatalog(t)
			c.path, c.endpoint = filepath.Join(t.TempDir(), "cache.json"), server.URL
			c.updated = time.Now().Add(-2 * modelsCatalogTTL)
			c.persist(c.providers, c.updated)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := c.RefreshAsync(ctx)
			awaitCatalog(t, entered)
			if mode == "cancel" {
				cancel()
			}
			awaitCatalog(t, done)
			awaitCatalog(t, c.RefreshAsync(context.Background()))
			if hits.Load() != 1 || externalHits.Load() != 0 {
				t.Fatalf("failure retried immediately or followed redirect: %d %d", hits.Load(), externalHits.Load())
			}
			for _, cached := range []*ModelCatalog{c, NewModelCatalog(c.path)} {
				if match, ok := cached.lookup("https://provider.example/v1", "org/model", "", ""); !ok || match.window != 131072 {
					t.Fatal("failure overwrote stale cache")
				}
			}
		})
	}
}

func TestModelsCatalogMalformedCacheIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	for _, data := range []string{`{`, `null`, `{"version":99}`, `{"version":1,"updated_at":"2099-01-01T00:00:00Z","providers":{"x":{}}}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if c := NewModelCatalog(path); c.providers != nil || !c.updated.IsZero() {
			t.Fatalf("invalid cache accepted: %s", data)
		}
	}
}

func TestModelsCatalogSelectionAndInputLimit(t *testing.T) {
	for _, mode := range []string{"catalog", "manual", "disabled", "missing"} {
		t.Run(mode, func(t *testing.T) {
			var gets atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					gets.Add(1)
					t.Error("capacity lookup queried the conversation service")
					w.WriteHeader(404)
					return
				}
				readRequest(t, r)
				replyJSON(w, "回答")
			}))
			defer server.Close()
			c := testModelCatalog(t)
			provider := c.providers["test"]
			provider.API = server.URL + "/v1"
			c.providers["test"] = provider
			cfg := Config{BaseURL: server.URL + "/v1", APIKey: "private-key", Model: "org/model", Persona: "测试助手", AutoContextWindow: mode != "manual", ContextWindow: 65536, ModelsDev: c, Memory: OpenMemory("")}
			if mode == "disabled" {
				cfg.ModelsDev = nil
			}
			if mode == "disabled" || mode == "manual" {
				c.updated = time.Now().Add(-2 * modelsCatalogTTL)
				c.endpoint = server.URL + "/api.json"
			}
			if mode == "missing" {
				cfg.Model = "unknown"
			}
			preview := cfg.Memory.ContextStatsForConfig(cfg)
			e, err := New(context.Background(), cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if e.ContextStats() != preview {
				t.Fatal("startup estimate and Engine disagree on catalog capacity")
			}
			if _, err := e.Reply(context.Background(), "问题"); err != nil {
				t.Fatal(err)
			}
			stats := e.ContextStats()
			if mode == "disabled" || mode == "manual" {
				c.mu.RLock()
				attempted := c.attempted
				c.mu.RUnlock()
				if !attempted.IsZero() {
					t.Fatal("disabled or manual mode downloaded external catalog")
				}
			}
			wantSource, wantWindow, wantInput := "fallback", 65536, 0
			switch mode {
			case "catalog":
				wantSource, wantWindow, wantInput = "modelsdev", 131072, 65536
			case "manual":
				wantSource = "manual"
			}
			if gets.Load() != 0 {
				t.Fatal("capacity lookup queried provider metadata")
			}
			if stats.ContextWindowSource != wantSource || stats.ContextWindow != wantWindow || stats.ContextInputLimit != wantInput || (wantInput > 0 && stats.InputBudget > wantInput) {
				t.Fatalf("incorrect catalog priority or budget: %+v", stats)
			}
			if mode == "catalog" {
				changed := cfg
				changed.ModelsDev = nil
				if got := cfg.Memory.ContextStatsForConfig(changed); got.ContextWindowSource != "fallback" || got.ContextWindow != cfg.ContextWindow || got.ContextWindowReference != "" {
					t.Fatalf("disabled catalog retained previous Engine statistics: %+v", got)
				}
				changed.AutoContextWindow, changed.ContextWindow = false, 8192
				if got := cfg.Memory.ContextStatsForConfig(changed); got.ContextWindowSource != "manual" || got.ContextWindow != 8192 || got.ContextInputLimit != 0 {
					t.Fatalf("manual mode retained previous Engine statistics: %+v", got)
				}
			}
			e.Clear()
			if got := e.ContextStats(); got.ContextInputLimit != stats.ContextInputLimit || got.InputBudget != stats.InputBudget || got.ContextWindowReference != stats.ContextWindowReference {
				t.Fatal("clear erased catalog capacity or input restriction")
			}
			if mode == "catalog" {
				// Tool-growth reserve must not bypass the independent input ceiling.
				payload, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "tool", "content": strings.Repeat("x", wantInput*3)}}})
				ctx := context.WithValue(context.Background(), requestScopeKey{}, requestScope{generation: e.generation, epoch: e.memoryEpoch})
				req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", bytes.NewReader(payload))
				_, _, err := e.budgetRequest(req)
				var failure *requestFailure
				if !errors.As(err, &failure) || failure.code != "context_limit" {
					t.Fatalf("input ceiling bypassed: %v", err)
				}
			}
		})
	}
}

func TestModelsCatalogColdDownloadDoesNotBlockReply(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var catalogHits, metadataHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api.json" {
			catalogHits.Add(1)
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Error("catalog request leaked credentials or configuration")
			}
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Write([]byte(testCatalogJSON))
			return
		}
		if r.Method == "GET" {
			metadataHits.Add(1)
			w.WriteHeader(404)
			return
		}
		req := readRequest(t, r)
		if req["model"] != "private-deployment" {
			t.Error("catalog mapping changed the actual request model")
		}
		replyJSON(w, "回答")
	}))
	defer server.Close()
	c := NewModelCatalog("")
	c.endpoint = server.URL + "/api.json"
	e, err := New(context.Background(), Config{BaseURL: server.URL + "/v1", APIKey: "private-key", Model: "private-deployment", Persona: "测试助手", AutoContextWindow: true, ModelsDev: c, ModelsDevProvider: "test", ModelsDevModel: "org/model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	awaitCatalog(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := e.Reply(ctx, "问题"); err != nil {
		t.Fatalf("cold catalog blocked chat: %v", err)
	}
	if stats := e.ContextStats(); stats.ContextWindowSource != "fallback" || stats.ContextWindow != 32768 {
		t.Fatalf("cold catalog did not fall back: %+v", stats)
	}
	done := c.RefreshAsync(context.Background())
	close(release)
	awaitCatalog(t, done)
	if _, err := e.Reply(context.Background(), "继续"); err != nil {
		t.Fatal(err)
	}
	if stats := e.ContextStats(); stats.ContextWindowSource != "modelsdev" || stats.ContextWindow != 131072 || stats.ContextWindowModel != "private-deployment" || stats.ContextWindowReference != "test/org/model" || catalogHits.Load() != 1 || metadataHits.Load() != 0 {
		t.Fatalf("downloaded catalog not adopted on next turn: %+v", stats)
	}
}
