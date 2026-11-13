package voice

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

type blockedDeviceDefaultTransport struct{ calls atomic.Int32 }

func (b *blockedDeviceDefaultTransport) RoundTrip(*http.Request) (*http.Response, error) {
	b.calls.Add(1)
	return nil, errors.New("device must not use process-default transport")
}

func TestConnectDeviceUsesExplicitDirectTransport(t *testing.T) {
	blocked := &blockedDeviceDefaultTransport{}
	original := http.DefaultTransport
	http.DefaultTransport = blocked
	t.Cleanup(func() { http.DefaultTransport = original })
	router, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	router.Close()
	ctx, cancel := context.WithTimeout(netproxy.WithRouter(context.Background(), router), 2*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/voice" {
			t.Errorf("unexpected voice device path %q", r.URL.Path)
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		device := testDevice{ws: ws, ctx: ctx, epoch: 42}
		if err := device.send(message{Type: "hello", SampleRate: sampleRate, Channels: 1, Encoding: "pcm_s16le", FrameSamples: frameSamples, Capabilities: []string{"capture", "playback"}}); err != nil {
			t.Error(err)
			return
		}
		ws.Read(ctx)
	}))
	defer server.Close()
	s := &session{ctx: ctx}
	_, readerDone, err := s.connectDevice(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.ws.CloseNow()
	if s.epoch != 42 || blocked.calls.Load() != 0 {
		t.Fatalf("device negotiation epoch=%d default transport calls=%d", s.epoch, blocked.calls.Load())
	}
	cancel()
	s.ws.CloseNow()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("direct device reader did not stop on cancellation")
	}
}
