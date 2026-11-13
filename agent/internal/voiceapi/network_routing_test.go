package voiceapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"aemeath/agent/internal/netproxy"
)

func TestMiMoClientUsesCurrentRequestRouter(t *testing.T) {
	c := New("http://provider.invalid/v1", "private-key")
	for _, transcript := range []string{"first route", "second route"} {
		t.Run(transcript, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "provider.invalid" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-key" {
					t.Errorf("unexpected routed MiMo request host=%s path=%s", r.Host, r.URL.Path)
				}
				fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, transcript)
			}))
			defer proxy.Close()
			router, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: proxy.URL})
			if err != nil {
				t.Fatal(err)
			}
			defer router.Close()
			out, err := c.Recognize(netproxy.WithRouter(context.Background(), router), []int16{1})
			if err != nil || out != transcript {
				t.Fatalf("transcript=%q err=%v", out, err)
			}
		})
	}
}
