package desktop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundBindingChecksExpectedTargetSynchronously(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		sendBackgroundInfo(w, backgroundInfo(nil, 0))
	}))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, nil)
	if _, err := s.BackgroundClient("previous-device:80"); !errors.Is(err, ErrOffline) {
		t.Fatal("wrong expected target admitted", err)
	}
	if _, err := s.BackgroundClient(""); !errors.Is(err, ErrOffline) {
		t.Fatal("empty expected target admitted", err)
	}
	client, err := s.BackgroundClient(s.Snapshot().Target)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("binding performed I/O")
	}
	// A handle may be reused for independent requests; it does not pin wg.
	for n := 0; n < 2; n++ {
		if _, err := client.GetBackground(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("reusable binding request count")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("unused binding pins Close")
	}
	if _, err := client.GetBackground(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestBackgroundBindingRejectsPutAndDeleteBeforeAdmissionAfterSwitch(t *testing.T) {
	data := backgroundJPEG(t)
	for _, mutation := range []string{"target", "connection", "context", "same-address-reconnect"} {
		for _, method := range []string{"PUT", "DELETE"} {
			t.Run(mutation+"/"+method, func(t *testing.T) {
				var oldRequests, newRequests atomic.Int32
				oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					oldRequests.Add(1)
					sendBackgroundInfo(w, backgroundInfo(nil, 0))
				}))
				t.Cleanup(oldServer.Close)
				newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					newRequests.Add(1)
					sendBackgroundInfo(w, backgroundInfo(nil, 0))
				}))
				t.Cleanup(newServer.Close)
				s := backgroundService(t, oldServer, nil)
				client, err := s.BackgroundClient(s.Snapshot().Target)
				if err != nil {
					t.Fatal(err)
				}
				// Model asynchronous work which has not yet entered the service. Do not
				// cancel the old context: admission must check identity, not only Err().
				replacement, cancel := context.WithCancel(context.Background())
				defer cancel()
				s.mu.Lock()
				switch mutation {
				case "target":
					s.state.Target = strings.TrimPrefix(newServer.URL, "http://")
				case "connection":
					s.conn = &fakeConn{}
				case "context":
					s.connCtx = replacement
				case "same-address-reconnect":
					s.conn = &fakeConn{}
					s.connCtx = replacement
				}
				s.mu.Unlock()
				if method == "PUT" {
					_, err = client.SetBackground(context.Background(), data, 0)
				} else {
					_, err = client.ClearBackground(context.Background(), 0)
				}
				if !errors.Is(err, ErrOffline) {
					t.Fatalf("stale handle admitted: %v", err)
				}
				// Read and post-error reconciliation must use the same connection binding.
				if _, err := client.GetBackground(context.Background()); !errors.Is(err, ErrOffline) {
					t.Fatal(err)
				}
				if _, err := client.GetBackgroundImage(context.Background()); !errors.Is(err, ErrOffline) {
					t.Fatal(err)
				}
				if oldRequests.Load() != 0 || newRequests.Load() != 0 {
					t.Fatalf("stale request side effect: old=%d new=%d", oldRequests.Load(), newRequests.Load())
				}
			})
		}
	}
}
