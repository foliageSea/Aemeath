package desktop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestBackgroundClientCurrentInvalidatesCompletedResponseOnConnectionChange(t *testing.T) {
	for _, change := range []string{"connection", "context", "both", "target", "disconnected", "connection-canceled", "service-canceled", "closed"} {
		t.Run(change, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				sendBackgroundInfo(w, backgroundInfo(nil, 0))
			}))
			t.Cleanup(server.Close)
			s := backgroundService(t, server, nil)
			// Use our own connection cancellation source without changing address.
			originalCtx, cancelOriginal := context.WithCancel(context.Background())
			defer cancelOriginal()
			s.mu.Lock()
			s.connCtx = originalCtx
			s.mu.Unlock()
			client, err := s.BackgroundClient(s.Snapshot().Target)
			if err != nil {
				t.Fatal(err)
			}
			if !client.Current() {
				t.Fatal("new binding is not current")
			}
			request, cancelRequest := context.WithCancel(context.Background())
			if _, err := client.GetBackground(request); err != nil {
				t.Fatal(err)
			}
			cancelRequest()
			if !client.Current() {
				t.Fatal("completed request cancellation invalidated connection binding")
			}
			if requests.Load() != 1 {
				t.Fatal("unexpected GET count")
			}
			replacement, cancelReplacement := context.WithCancel(context.Background())
			defer cancelReplacement()
			s.mu.Lock()
			switch change {
			case "connection":
				s.conn = &fakeConn{}
			case "context":
				s.connCtx = replacement
			case "both":
				s.conn = &fakeConn{}
				s.connCtx = replacement
			case "target":
				s.state.Target = "different-device:80"
			case "disconnected":
				s.state.Connected = false
			case "connection-canceled":
				cancelOriginal()
			case "service-canceled":
				s.cancel()
			case "closed":
				s.closed = true
			}
			s.mu.Unlock()
			if client.Current() {
				t.Fatal("completion could apply metadata from an obsolete connection")
			}
			if requests.Load() != 1 {
				t.Fatal("Current performed I/O")
			}
			// Restore closed admission so the normal registered cleanup can join workers.
			if change == "closed" {
				s.mu.Lock()
				s.closed = false
				s.mu.Unlock()
			}
		})
	}
}

func TestBackgroundClientCurrentHandlesUninitializedBinding(t *testing.T) {
	var absent *backgroundClient
	if absent.Current() || (&backgroundClient{}).Current() {
		t.Fatal("uninitialized handle current")
	}
}
