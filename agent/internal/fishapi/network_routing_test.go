package fishapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"aemeath/agent/internal/netproxy"
	"github.com/coder/websocket"
)

func TestFishStreamUsesRequestRouterAndPreservesDuplexBody(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "fish.invalid" || r.URL.Path != "/v1/tts/live" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected routed Fish handshake host=%s path=%s", r.Host, r.URL.Path)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		if consumeInput(t, ctx, conn) && sendAudio(t, ctx, conn, []byte{1, 0, 2, 0}) {
			finish(t, ctx, conn)
		}
	}))
	defer proxy.Close()
	router, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	client, err := New(Config{BaseURL: "http://fish.invalid", APIKey: "test-secret", ReferenceID: "voice-id"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := netproxy.WithRouter(testContext(t), router)
	var samples []int16
	err = client.Stream(ctx, textInput("test"), func(pcm []int16) error {
		samples = append(samples, pcm...)
		return nil
	})
	if err != nil || calls.Load() != 1 || len(samples) != 2 || samples[0] != 1 || samples[1] != 2 {
		t.Fatalf("stream samples=%v requests=%d err=%v", samples, calls.Load(), err)
	}
}
