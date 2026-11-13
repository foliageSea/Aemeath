package brain

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/netproxy"
)

func testProxyRouter(t *testing.T, proxyURL string) *netproxy.Router {
	t.Helper()
	router, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: proxyURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Close)
	return router
}

func TestDetachedEnginePreservesRoutingForCatalogAndCompression(t *testing.T) {
	var catalogs, summaries atomic.Int32
	catalogEntered, catalogRelease := make(chan struct{}, 1), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "catalog.invalid":
			catalogs.Add(1)
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
				t.Error("anonymous catalog leaked provider credentials")
			}
			catalogEntered <- struct{}{}
			select {
			case <-catalogRelease:
				io.WriteString(w, testCatalogJSON)
			case <-r.Context().Done():
			}
		case "provider.invalid":
			if !isSummaryRequest(readRequest(t, r)) {
				t.Error("expected background compression request")
			}
			summaries.Add(1)
			replyJSON(w, "Retain the user's preferences.")
		default:
			t.Errorf("unexpected proxy target %q", r.Host)
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer proxy.Close()
	router := testProxyRouter(t, proxy.URL)
	parent, cancel := context.WithCancel(netproxy.WithRouter(context.Background(), router))
	defer cancel()
	catalog := NewModelCatalog("")
	catalog.endpoint = "http://catalog.invalid/api.json"
	e, err := New(parent, Config{BaseURL: "http://provider.invalid/v1", APIKey: "private-key", Model: "test", Persona: "Test assistant", ContextWindow: 8192, AutoContextWindow: true, ModelsDev: catalog}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	select {
	case <-catalogEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("background catalog did not use the constructor's router")
	}
	done := catalog.RefreshAsync(e.lifeCtx)
	cancel()
	if e.lifeCtx.Err() != nil {
		t.Fatal("constructor cancellation canceled engine lifetime")
	}
	close(catalogRelease)
	awaitCatalog(t, done)
	catalog.mu.RLock()
	loaded := !catalog.updated.IsZero()
	catalog.mu.RUnlock()
	if !loaded || catalogs.Load() != 1 {
		t.Fatalf("detached catalog not loaded: loaded=%v requests=%d", loaded, catalogs.Load())
	}
	seedContext(e, 12)
	awaitCompression(t, e.startCompression(e.lifeCtx))
	if stats := e.ContextStats(); summaries.Load() != 1 || stats.Compressions != 1 {
		t.Fatalf("background compression lost router: requests=%d stats=%+v", summaries.Load(), stats)
	}
	e.Close()
	if !errors.Is(e.lifeCtx.Err(), context.Canceled) {
		t.Fatal("engine Close did not cancel detached lifetime")
	}
}

func TestCatalogNetworkChangeBypassesFailureCooldownButKeepsFreshData(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, testCatalogJSON)
	}))
	defer proxy.Close()
	router := testProxyRouter(t, proxy.URL)
	ctx := netproxy.WithRouter(context.Background(), router)
	catalog := NewModelCatalog("")
	catalog.endpoint = "http://catalog.invalid/api.json"
	awaitCatalog(t, catalog.RefreshAsync(ctx))
	awaitCatalog(t, catalog.RefreshAsync(ctx))
	if calls.Load() != 1 {
		t.Fatal("normal refresh ignored failure cooldown")
	}
	awaitCatalog(t, catalog.RefreshAfterNetworkChange(ctx))
	catalog.mu.RLock()
	updated := catalog.updated
	loaded := usableCatalog(catalog.providers)
	catalog.mu.RUnlock()
	if calls.Load() != 2 || updated.IsZero() || !loaded {
		t.Fatalf("network change failed to retry: requests=%d loaded=%v", calls.Load(), loaded)
	}
	awaitCatalog(t, catalog.RefreshAfterNetworkChange(ctx))
	catalog.mu.RLock()
	unchanged := catalog.updated.Equal(updated) && usableCatalog(catalog.providers)
	catalog.mu.RUnlock()
	if calls.Load() != 2 || !unchanged {
		t.Fatal("network change evicted or refetched fresh valid data")
	}
}

func TestCatalogNetworkChangeCancelsSupersededRefresh(t *testing.T) {
	oldEntered, oldCanceled := make(chan struct{}), make(chan struct{})
	oldProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(oldEntered)
		<-r.Context().Done()
		close(oldCanceled)
	}))
	defer oldProxy.Close()
	newEntered, newRelease := make(chan struct{}), make(chan struct{})
	newProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(newEntered)
		select {
		case <-newRelease:
			io.WriteString(w, testCatalogJSON)
		case <-r.Context().Done():
		}
	}))
	defer newProxy.Close()
	router := testProxyRouter(t, oldProxy.URL)
	ctx, cancel := context.WithCancel(netproxy.WithRouter(context.Background(), router))
	defer cancel()
	catalog := NewModelCatalog("")
	catalog.endpoint = "http://catalog.invalid/api.json"
	oldDone := catalog.RefreshAsync(ctx)
	awaitCatalog(t, oldEntered)
	if err := router.Update(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: newProxy.URL}); err != nil {
		t.Fatal(err)
	}
	newDone := catalog.RefreshAfterNetworkChange(ctx)
	awaitCatalog(t, newEntered)
	awaitCatalog(t, oldDone)
	awaitCatalog(t, oldCanceled)
	if catalog.RefreshAsync(ctx) != newDone {
		t.Fatal("superseded worker cleared replacement refresh")
	}
	close(newRelease)
	awaitCatalog(t, newDone)
	catalog.mu.RLock()
	loaded := usableCatalog(catalog.providers)
	catalog.mu.RUnlock()
	if !loaded {
		t.Fatal("replacement refresh did not publish metadata")
	}
}
