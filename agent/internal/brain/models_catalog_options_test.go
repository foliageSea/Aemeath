package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelCatalogOptionsSortedValidAndIsolated(t *testing.T) {
	c := testModelCatalog(t)
	var extra map[string]catalogProvider
	if err := json.Unmarshal([]byte(`{"z-provider":{"models":{"z":{"id":"z","limit":{"context":4096}},"A":{"id":"A","limit":{"context":4096}},"a":{"id":"a","limit":{"context":4096}}}},"empty":{"models":{}},"invalid":{"models":{"bad":{"id":"bad","limit":{"context":1}}}}}`), &extra); err != nil {
		t.Fatal(err)
	}
	for id, p := range extra {
		c.providers[id] = p
	}
	c.providers[""] = c.providers["test"]
	c.providers[" \t"] = c.providers["test"]
	want := []ModelCatalogProvider{{ID: "openai", Models: []string{"exact"}}, {ID: "test", Models: []string{"org/model"}}, {ID: "z-provider", Models: []string{"A", "a", "z"}}}
	got, err := c.Options(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Options=%+v err=%v", got, err)
	}
	got[0].ID = "mutated"
	got[1].Models[0] = "mutated"
	got = append(got, ModelCatalogProvider{ID: "mutated"})
	again, err := c.Options(nil)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("caller changed catalog: %+v err=%v", again, err)
	}
	c.mu.Lock()
	delete(c.providers, "test")
	c.mu.Unlock()
	if !reflect.DeepEqual(again, want) {
		t.Fatal("catalog changed prior snapshot")
	}
}

func TestModelCatalogOptionsFreshAndStaleCache(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		close(entered)
		<-release
		http.Error(w, "private upstream details", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	c := testModelCatalog(t)
	c.endpoint = server.URL
	fresh, err := c.Options(context.Background())
	if err != nil || len(fresh) != 2 || hits.Load() != 0 {
		t.Fatalf("fresh=%v err=%v hits=%d", fresh, err, hits.Load())
	}
	c.mu.Lock()
	c.updated = time.Now().Add(-2 * modelsCatalogTTL)
	c.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		got, err := c.Options(context.Background())
		if err == nil && !reflect.DeepEqual(got, fresh) {
			err = errors.New("stale snapshot changed")
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cached options blocked on refresh")
	}
	awaitCatalog(t, entered)
	c.mu.RLock()
	done := c.refreshing
	c.mu.RUnlock()
	if c.RefreshAsync(context.Background()) != done {
		t.Fatal("Options did not reuse refresh machinery")
	}
	close(release)
	awaitCatalog(t, done)
	got, err := c.Options(context.Background())
	if err != nil || !reflect.DeepEqual(got, fresh) || hits.Load() != 1 {
		t.Fatalf("offline options=%v err=%v hits=%d", got, err, hits.Load())
	}
}

func TestModelCatalogOptionsFirstDownload(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(testCatalogJSON))
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	c := NewModelCatalog("")
	c.endpoint = server.URL
	result := make(chan []ModelCatalogProvider, 1)
	errs := make(chan error, 1)
	go func() { got, err := c.Options(context.Background()); result <- got; errs <- err }()
	awaitCatalog(t, entered)
	select {
	case <-result:
		t.Fatal("returned before first refresh")
	default:
	}
	done := c.RefreshAsync(context.Background())
	close(release)
	awaitCatalog(t, done)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if got := <-result; len(got) != 2 || got[0].ID != "openai" {
		t.Fatalf("downloaded options=%+v", got)
	}
}

func TestModelCatalogOptionsCancellation(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(exited)
	}))
	defer server.Close()
	c := NewModelCatalog("")
	c.endpoint = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := c.Options(ctx); result <- err }()
	awaitCatalog(t, entered)
	done := c.RefreshAsync(context.Background())
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock Options")
	}
	awaitCatalog(t, done)
	awaitCatalog(t, exited)
	if len(c.optionsSnapshot()) != 0 {
		t.Fatal("canceled fetch published catalog")
	}
	if got, err := c.Options(ctx); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled=%v, %v", got, err)
	}
}

func TestModelCatalogOptionsWaiterDeadlineDoesNotCancelExistingRefresh(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(testCatalogJSON))
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	c := NewModelCatalog("")
	c.endpoint = server.URL
	done := c.RefreshAsync(context.Background())
	awaitCatalog(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got, err := c.Options(ctx); got != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v err=%v", got, err)
	}
	select {
	case <-done:
		t.Fatal("waiter canceled existing shared refresh")
	default:
	}
	close(release)
	awaitCatalog(t, done)
	if got, err := c.Options(context.Background()); err != nil || len(got) != 2 {
		t.Fatalf("shared refresh=%v err=%v", got, err)
	}
}

func TestModelCatalogOptionsNoCatalogSafeErrorAndRetry(t *testing.T) {
	for _, body := range []string{"{}", "private malformed details", `{"provider":{"models":{"bad":{"id":"bad","limit":{"context":1}}}}}`} {
		t.Run(body, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte(body)) }))
			defer server.Close()
			c := NewModelCatalog("")
			c.endpoint = server.URL + "/private-token"
			for range 2 {
				got, err := c.Options(context.Background())
				if got != nil || err == nil || err.Error() != "models.dev catalog unavailable" {
					t.Fatalf("Options=%v err=%v", got, err)
				}
			}
			if hits.Load() != 1 {
				t.Fatalf("failure ignored refresh cooldown: %d", hits.Load())
			}
		})
	}
	var c *ModelCatalog
	if got, err := c.Options(nil); got != nil || err == nil || err.Error() != "models.dev catalog unavailable" {
		t.Fatalf("nil catalog=%v err=%v", got, err)
	}
	c = NewModelCatalog("")
	c.endpoint = "://private-url-with-token"
	if got, err := c.Options(context.Background()); got != nil || err == nil || err.Error() != "models.dev catalog unavailable" {
		t.Fatalf("transport error=%v err=%v", got, err)
	}
}
