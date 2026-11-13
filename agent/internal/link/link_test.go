package link

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/netproxy"
	"github.com/coder/websocket"
)

type blockedDefaultTransport struct{ calls atomic.Int32 }

func (b *blockedDefaultTransport) RoundTrip(*http.Request) (*http.Response, error) {
	b.calls.Add(1)
	return nil, errors.New("device must not use process-default transport")
}

func TestDialUsesExplicitDirectTransport(t *testing.T) {
	blocked := &blockedDefaultTransport{}
	original := http.DefaultTransport
	http.DefaultTransport = blocked
	t.Cleanup(func() { http.DefaultTransport = original })
	// Even a closed request-scoped router must not affect a device connection.
	router, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	router.Close()
	ctx, cancel := context.WithTimeout(netproxy.WithRouter(context.Background(), router), 2*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			t.Errorf("unexpected device path %q", r.URL.Path)
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"screen_state"}`)); err != nil {
			t.Error(err)
			return
		}
		ws.Read(ctx) // process the client's close handshake
	}))
	defer server.Close()
	conn, err := Dial(ctx, strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case message := <-conn.Incoming():
		if string(message) != `{"type":"screen_state"}` {
			t.Fatalf("device message=%q", message)
		}
	case <-ctx.Done():
		t.Fatal("device reader did not receive direct websocket data")
	}
	if blocked.calls.Load() != 0 {
		t.Fatal("device dial consulted process-default transport")
	}
}
