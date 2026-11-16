package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/netproxy"
)

func TestServiceModelCatalogOptionsOwnCache(t *testing.T) {
	for _, id := range []string{"service-a", "service-b"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			data := fmt.Sprintf(`{"version":1,"updated_at":%q,"providers":{%q:{"api":"https://private-provider.invalid/v1","models":{"model":{"id":"model","limit":{"context":8192}},"invalid":{"id":"invalid","limit":{"context":1}}}}}}`, time.Now().UTC().Format(time.RFC3339Nano), id)
			if err := os.WriteFile(filepath.Join(dir, "models_dev_cache.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			settings, err := normalizeSettings(Settings{})
			if err != nil {
				t.Fatal(err)
			}
			s := newService(filepath.Join(dir, "settings.json"), settings, testDeps())
			defer s.Close()
			got, err := s.ModelCatalogOptions(nil)
			want := []brain.ModelCatalogProvider{{ID: id, Models: []string{"model"}}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("own cache=%+v err=%v", got, err)
			}
			encoded, err := json.Marshal(got)
			if err != nil || string(encoded) != fmt.Sprintf(`[{"id":%q,"models":["model"]}]`, id) {
				t.Fatalf("public result leaked metadata: %s err=%v", encoded, err)
			}
			got[0].Models[0] = "mutated"
			again, err := s.ModelCatalogOptions(context.Background())
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatalf("service snapshot not isolated: %+v err=%v", again, err)
			}
		})
	}
}

func TestServiceModelCatalogOptionsSavedProxyPolicy(t *testing.T) {
	var hits, wrongHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodConnect || r.Host != "models.dev:443" {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.Host)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Errorf("catalog leaked configuration: %v", r.Header)
		}
		http.Error(w, "private proxy error", http.StatusBadGateway)
	}))
	defer proxy.Close()
	wrongProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { wrongHits.Add(1); w.WriteHeader(http.StatusBadGateway) }))
	defer wrongProxy.Close()
	settings, err := normalizeSettings(Settings{Network: netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: proxy.URL}})
	if err != nil {
		t.Fatal(err)
	}
	s := newService(filepath.Join(t.TempDir(), "settings.json"), settings, testDeps())
	defer s.Close()
	wrongRouter, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: wrongProxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer wrongRouter.Close()
	ctx, cancel := context.WithTimeout(netproxy.WithRouter(context.Background(), wrongRouter), time.Second)
	defer cancel()
	got, err := s.ModelCatalogOptions(ctx)
	if got != nil || err == nil || err.Error() != "models.dev catalog unavailable" {
		t.Fatalf("proxy failure=%v err=%v", got, err)
	}
	if hits.Load() != 1 || wrongHits.Load() != 0 {
		t.Fatalf("did not use service policy: saved=%d caller=%d", hits.Load(), wrongHits.Load())
	}
}

func TestServiceModelCatalogOptionsStaleRefreshOutlivesCaller(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.WriteHeader(http.StatusBadGateway)
		case <-r.Context().Done():
		}
	}))
	defer proxy.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	settings, err := normalizeSettings(Settings{Network: netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: proxy.URL}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	data := fmt.Sprintf(`{"version":1,"updated_at":%q,"providers":{"cached":{"models":{"model":{"id":"model","limit":{"context":8192}}}}}}`, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, "models_dev_cache.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s := newService(filepath.Join(dir, "settings.json"), settings, testDeps())
	defer s.Close()
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	got, err := s.ModelCatalogOptions(caller)
	if err != nil || len(got) != 1 || got[0].ID != "cached" {
		t.Fatalf("stale options=%v err=%v", got, err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stale refresh did not start")
	}
	done := s.catalog.RefreshAsync(s.ctx)
	cancel()
	select {
	case <-done:
		t.Fatal("returning/canceling UI waiter canceled shared stale refresh")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("service refresh did not finish")
	}
	again, err := s.ModelCatalogOptions(context.Background())
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("failed refresh lost stale snapshot: %v err=%v", again, err)
	}
}

func TestServiceModelCatalogOptionsNilClosedAndCanceled(t *testing.T) {
	for _, s := range []*Service{nil, {}} {
		got, err := s.ModelCatalogOptions(nil)
		if got != nil || err == nil || err.Error() != "models.dev catalog unavailable" {
			t.Fatalf("nil service/catalog=%v err=%v", got, err)
		}
	}
	s := &Service{closed: true}
	if _, err := s.ModelCatalogOptions(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := (&Service{catalog: brain.NewModelCatalog("")}).ModelCatalogOptions(ctx); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled=%v err=%v", got, err)
	}
}
