package netproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustRouter(t *testing.T, c Config) *Router {
	t.Helper()
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}
func request(t *testing.T, rt http.RoundTripper, r *Router, raw string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	// Retain the context until cleanup so streaming/upgrade bodies stay usable.
	t.Cleanup(cancel)
	if r != nil {
		ctx = WithRouter(ctx, r)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rt.RoundTrip(req)
}
func text(t *testing.T, resp *http.Response, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func getText(t *testing.T, rt http.RoundTripper, r *Router, raw string) string {
	t.Helper()
	resp, err := request(t, rt, r, raw)
	return text(t, resp, err)
}
func labelProxy(t *testing.T, label string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "%s|%s", label, r.RemoteAddr) }))
	t.Cleanup(s.Close)
	return s
}
func localTemplate(addr string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
}

func TestRoutingUpdatesAndPoolIsolation(t *testing.T) {
	p := labelProxy(t, "proxy")
	origin := labelProxy(t, "direct")
	template := localTemplate(origin.Listener.Addr().String())
	// Explicitly map proxy dials to the real proxy and public host dials to origin.
	template.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != p.Listener.Addr().String() {
			addr = origin.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	rt := Transport(template)
	a := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: p.URL})
	b := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: p.URL})
	first := getText(t, rt, a, "http://public.example/resource")
	if !strings.HasPrefix(first, "proxy|") {
		t.Fatal(first)
	}
	if again := getText(t, Transport(template), a, "http://public.example/resource"); again != first {
		t.Fatalf("same template did not share owned pool: %s vs %s", first, again)
	}
	if other := getText(t, rt, b, "http://public.example/resource"); other == first {
		t.Fatal("router instances shared a connection")
	}
	if err := a.Update(Config{Mode: ModeDirect}); err != nil {
		t.Fatal(err)
	}
	if got := getText(t, rt, a, "http://public.example/resource"); !strings.HasPrefix(got, "direct|") {
		t.Fatal(got)
	}
	if err := a.Update(Config{Mode: ModeProxy, ProxyURL: p.URL}); err != nil {
		t.Fatal(err)
	}
	if after := getText(t, rt, a, "http://public.example/resource"); after == first || !strings.HasPrefix(after, "proxy|") {
		t.Fatalf("old generation reused: %s", after)
	}
	if err := a.Update(Config{Mode: ModeProxy, ProxyURL: "bad secret"}); err == nil {
		t.Fatal("invalid update accepted")
	}
	if got := getText(t, rt, a, "http://public.example/resource"); !strings.HasPrefix(got, "proxy|") {
		t.Fatal("invalid update changed routing")
	}
	if template.Proxy != nil {
		t.Fatal("template mutated")
	}
}

func TestTemplatePointerAndNilStability(t *testing.T) {
	if Transport(nil) != Transport(nil) || DirectTransport() != DirectTransport() {
		t.Fatal("shared transport wrappers are not stable")
	}
	origin := labelProxy(t, "origin")
	r := mustRouter(t, Config{Mode: ModeDirect})
	one := localTemplate(origin.Listener.Addr().String())
	two := localTemplate(origin.Listener.Addr().String())
	a := getText(t, Transport(one), r, "http://public.example")
	b := getText(t, Transport(two), r, "http://public.example")
	if a == b {
		t.Fatal("different templates shared pools")
	}
	a = getText(t, Transport(nil), r, origin.URL)
	b = getText(t, Transport(nil), r, origin.URL)
	if a != b {
		t.Fatal("nil template is not stable")
	}
}

func TestDirectAndLocalBypassIgnoreEnvironment(t *testing.T) {
	clearProxyEnv(t)
	p := labelProxy(t, "proxy")
	origin := labelProxy(t, "direct")
	t.Setenv("HTTP_PROXY", p.URL)
	r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: p.URL})
	if got := getText(t, DirectTransport(), r, origin.URL); !strings.HasPrefix(got, "direct|") {
		t.Fatal(got)
	}
	direct := mustRouter(t, Config{Mode: ModeDirect})
	if got := getText(t, Transport(localTemplate(origin.Listener.Addr().String())), direct, "http://public.example"); !strings.HasPrefix(got, "direct|") {
		t.Fatal(got)
	}
	if got := getText(t, Transport(nil), r, origin.URL); !strings.HasPrefix(got, "direct|") {
		t.Fatalf("local literal was proxied: %s", got)
	}
	for _, host := range []string{"localhost.", "child.localhost", "printer.local"} {
		if got := getText(t, Transport(localTemplate(origin.Listener.Addr().String())), r, "http://"+host); !strings.HasPrefix(got, "direct|") {
			t.Fatalf("local hostname was proxied: %s", got)
		}
	}
	// No context router defaults to system mode and sees fresh environment settings.
	if got := getText(t, Transport(nil), nil, "http://public.example"); !strings.HasPrefix(got, "proxy|") {
		t.Fatal(got)
	}
}

func TestProxyFailureDoesNotFallback(t *testing.T) {
	var directDials atomic.Int32
	template := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "127.0.0.1:1" {
			directDials.Add(1)
		}
		return nil, errors.New("test dial rejected")
	}}
	r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: "http://user:secret@127.0.0.1:1"})
	_, err := request(t, Transport(template), r, "http://public.example")
	if err == nil || strings.Contains(err.Error(), "secret") || directDials.Load() != 0 {
		t.Fatalf("proxy failed unsafely: %v, direct dials %d", err, directDials.Load())
	}
}

func TestCloseCancelAndConcurrentUpdates(t *testing.T) {
	origin := labelProxy(t, "origin")
	rt := Transport(nil)
	r := mustRouter(t, Config{Mode: ModeDirect})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				req, _ := http.NewRequestWithContext(WithRouter(context.Background(), r), "GET", origin.URL, nil)
				resp, err := rt.RoundTrip(req)
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				} else if !errors.Is(err, ErrClosed) {
					t.Errorf("concurrent request: %v", err)
				}
			}
		}()
	}
	for i := 0; i < 12; i++ {
		if err := r.Update(Config{Mode: ModeDirect}); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	r.Close()
	wg.Wait()
	if _, err := request(t, rt, r, origin.URL); !errors.Is(err, ErrClosed) {
		t.Fatalf("request after close: %v", err)
	}
	if err := r.Update(Config{Mode: ModeDirect}); !errors.Is(err, ErrClosed) {
		t.Fatalf("update after close: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", origin.URL, nil)
	if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
}

func TestRetiredStreamingResponseFinishesAndClosesPool(t *testing.T) {
	unblock := make(chan struct{})
	closed := make(chan struct{}, 4)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		<-unblock
		w.Write([]byte("last"))
	}))
	origin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	origin.Start()
	defer origin.Close()
	r := mustRouter(t, Config{Mode: ModeDirect})
	resp, err := request(t, Transport(nil), r, origin.URL)
	if err != nil {
		close(unblock)
		t.Fatal(err)
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		close(unblock)
		t.Fatal(err)
	}
	r.Close()
	close(unblock)
	rest, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(first)+string(rest) != "firstlast" {
		t.Fatalf("active response interrupted: %q %v", rest, err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("retired connection was not closed on body release")
	}
}

func TestNativeTTLChangesProxyPoolKey(t *testing.T) {
	clearProxyEnv(t)
	one, two := labelProxy(t, "one"), labelProxy(t, "two")
	r := mustRouter(t, Config{})
	endpoint := one.URL
	r.native.read = func(context.Context) (nativeConfig, error) { return nativeConfig{HTTP: endpoint}, nil }
	rt := Transport(nil)
	if got := getText(t, rt, r, "http://public.example"); !strings.HasPrefix(got, "one|") {
		t.Fatal(got)
	}
	endpoint = two.URL
	r.native.mu.Lock()
	r.native.expires = time.Time{}
	r.native.mu.Unlock()
	if got := getText(t, rt, r, "http://public.example"); !strings.HasPrefix(got, "two|") {
		t.Fatalf("stale native pool route: %s", got)
	}
	r.native.mu.Lock()
	r.native.expires = time.Time{}
	r.native.mu.Unlock()
	r.native.read = func(context.Context) (nativeConfig, error) { return nativeConfig{Auto: true}, nil }
	_, err := request(t, rt, r, "http://public.example")
	if !errors.Is(err, errNativeUnsupported) {
		t.Fatalf("automatic proxy direct fallback: %v", err)
	}
}

func TestProxyCallbackUsesSchemeAndBypass(t *testing.T) {
	clearProxyEnv(t)
	p := labelProxy(t, "proxy")
	r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: p.URL})
	rt := Transport(nil)
	_ = getText(t, rt, r, "http://public.example")
	r.mu.Lock()
	tr := r.current.transports[sharedTemplate]
	r.mu.Unlock()
	for _, raw := range []string{"http://127.0.0.1", "https://[::1]", "http://192.168.0.1", "http://foo.localhost."} {
		u, _ := url.Parse(raw)
		p, err := tr.Proxy(&http.Request{URL: u})
		if err != nil || p != nil {
			t.Fatalf("local bypass %s: %v %v", raw, p, err)
		}
	}
}
