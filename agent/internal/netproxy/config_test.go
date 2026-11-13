package netproxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	for _, mode := range []string{"", "system", "direct", " SYSTEM "} {
		got, err := Normalize(Config{Mode: mode, ProxyURL: "not a URL\nsecret"})
		if err != nil || got.ProxyURL != "not a URL\nsecret" {
			t.Fatalf("inactive setting lost: %#v %v", got, err)
		}
	}
	for _, raw := range []string{"http://host:80", "https://user:p%40ss@host:443", "socks5://user:pass@[::1]:1080"} {
		if _, err := Normalize(Config{Mode: ModeProxy, ProxyURL: raw}); err != nil {
			t.Errorf("valid URL rejected: %v", err)
		}
	}
	for _, raw := range []string{"", "host:80", "ftp://host:80", "http://host", "http://:80", "http://host:0", "http://host:65536", "http://host:-1", "http://host:+80", "http://host:80/", "http://host:80/path", "http://host:80?", "http://host:80#", "http://host:80\n", "http://user:secret@host:bad", "http://[not-ip]:80", "http://::1:80", "http://host:80/%00", "http://user%0a:pass@host:80", "socks5://user:pass%00@host:1080"} {
		if _, err := Normalize(Config{Mode: ModeProxy, ProxyURL: raw}); err == nil {
			t.Errorf("invalid proxy accepted: %q", raw)
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatal("credential in validation error")
		}
	}
	if _, err := Normalize(Config{Mode: "unknown"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestLocalBypass(t *testing.T) {
	for _, host := range []string{"localhost", "a.localhost", "LOCALHOST.", "foo.local.", "127.3.2.1", "::1", "::ffff:127.0.0.1", "10.1.2.3", "172.16.3.2", "192.168.1.4", "169.254.1.2", "fe80::1", "fc00::1"} {
		if !localHost(host) {
			t.Errorf("local host not bypassed: %s", host)
		}
	}
	for _, host := range []string{"example.com", "localhost.example.com", "notlocalhost", "8.8.8.8", "172.32.0.1", "2001:4860:4860::8888"} {
		if localHost(host) {
			t.Errorf("public host bypassed: %s", host)
		}
	}
}

func TestNativeParsing(t *testing.T) {
	mac := "<dictionary> {\n HTTPEnable : 1\n HTTPProxy : proxy.example\n HTTPPort : 8080\n HTTPSEnable : 0\n HTTPSProxy : ignored\n SOCKSEnable : 1\n SOCKSProxy : ::1\n SOCKSPort : 1080\n ProxyAutoConfigEnable : 0\n ProxyAutoDiscoveryEnable : 1\n ExcludeSimpleHostnames : 1\n ExceptionsList : <array> {\n 0 : *.example.org\n 1 : exact.test:443\n }\n}\n"
	n, err := parseSCUtil(mac)
	if err != nil || n.HTTP != "http://proxy.example:8080" || n.HTTPS != "" || n.SOCKS != "socks5://[::1]:1080" || !n.Auto || len(n.Bypass) != 3 {
		t.Fatalf("mac parse: %#v %v", n, err)
	}
	for _, bad := range []string{"", "garbage", "<dictionary>", "<dictionary> {", "<dictionary> {\n}\ngarbage", "<dictionary> {\n}\n<dictionary> {\n}", "<dictionary> {\n HTTPEnable : 1\n}", "<dictionary> {\n HTTPEnable : 1\n HTTPProxy : proxy\n HTTPPort : 65536\n}"} {
		if _, err := parseSCUtil(bad); err == nil {
			t.Fatalf("bad mac settings accepted: %s", bad)
		}
	}
	n, err = parseWindowsProxy("http=one:80;https=two:443;socks=three:1080", "<local>;*.example.org;exact.test:443", true, true)
	if err != nil || n.HTTP != "http://one:80" || n.HTTPS != "http://two:443" || n.SOCKS != "socks5://three:1080" || len(n.Bypass) != 3 || !n.Auto {
		t.Fatalf("windows parse: %#v %v", n, err)
	}
	n, err = parseWindowsProxy("all:8080", "", true, false)
	if err != nil || n.HTTP != n.HTTPS {
		t.Fatalf("global proxy: %#v %v", n, err)
	}
	n, err = parseWindowsProxy("bad proxy", "", false, true)
	if err != nil || !n.Auto || n.HTTP != "" {
		t.Fatalf("inactive native URL: %#v %v", n, err)
	}
	for _, bad := range []string{"", "http=host", "http=user:secret@host:bad", "http=host:80/"} {
		if _, err := parseWindowsProxy(bad, "", true, false); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("bad native settings: %v", err)
		}
	}
	u, _ := url.Parse("https://public.example")
	if _, err := (nativeConfig{Auto: true}).proxy(u); !errors.Is(err, errNativeUnsupported) {
		t.Fatalf("PAC fallback: %v", err)
	}
}

func TestNativeBypass(t *testing.T) {
	tests := []struct {
		raw, entry string
		want       bool
	}{
		{"http://printer", "<local>", true}, {"http://printer.example", "<local>", false},
		{"http://8.8.8.8", "<local>", false}, {"http://a.example.org", "*.example.org", true},
		{"http://example.org", "*.example.org", false}, {"https://EXACT.test.", "exact.test:443", true},
		{"http://exact.test", "exact.test:443", false}, {"https://a.example.org:8443", "*.example.org:8443", true},
		{"https://[2001:db8::1]:8443", "[2001:db8::1]:8443", true},
	}
	for _, tt := range tests {
		u, _ := url.Parse(tt.raw)
		if got := nativeBypass(u, []string{tt.entry}); got != tt.want {
			t.Errorf("bypass(%s,%s)=%v", tt.raw, tt.entry, got)
		}
	}
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(key, "")
	}
}

func TestSystemEnvironmentAndNative(t *testing.T) {
	clearProxyEnv(t)
	r, _ := New(Config{})
	defer r.Close()
	var reads atomic.Int32
	r.native.read = func(context.Context) (nativeConfig, error) {
		reads.Add(1)
		return nativeConfig{HTTP: "http://native:8000", HTTPS: "http://native-tls:8001", Auto: true}, nil
	}
	pick := func(raw, want string) {
		t.Helper()
		req, _ := http.NewRequest("GET", raw, nil)
		got, err := r.systemProxy(req)
		if err != nil {
			t.Fatal(err)
		}
		s := ""
		if got != nil {
			s = got.String()
		}
		if s != want {
			t.Fatalf("proxy(%s)=%s want %s", raw, s, want)
		}
	}
	pick("http://public.example", "http://native:8000")
	t.Setenv("HTTP_PROXY", "http://environment:9000")
	pick("http://public.example", "http://environment:9000")
	pick("https://public.example", "http://native-tls:8001")
	t.Setenv("HTTP_PROXY", "http://changed:9001")
	pick("http://public.example", "http://changed:9001")
	t.Setenv("NO_PROXY", "public.example")
	pick("https://public.example", "")
	pick("http://public.example", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("HTTPS_PROXY", "http://tls-env:9002")
	pick("https://public.example", "http://tls-env:9002")
	t.Setenv("HTTP_PROXY", "http://user:secret@host:bad")
	req, _ := http.NewRequest("GET", "http://public.example", nil)
	if _, err := r.systemProxy(req); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("bad env silently bypassed/leaked: %v", err)
	}
	if reads.Load() != 1 {
		t.Fatalf("native not cached: %d", reads.Load())
	}
}

func TestNativeCacheCancellationAndExpiry(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	c := &nativeCache{read: func(ctx context.Context) (nativeConfig, error) {
		close(started)
		select {
		case <-finish:
			return nativeConfig{HTTP: "http://one:80"}, nil
		case <-ctx.Done():
			return nativeConfig{}, ctx.Err()
		}
	}}
	done := make(chan error, 1)
	go func() { _, err := c.get(context.Background()); done <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait cancellation: %v", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.expires = time.Time{}
	c.read = func(context.Context) (nativeConfig, error) { return nativeConfig{HTTP: "http://two:80"}, nil }
	c.mu.Unlock()
	n, err := c.get(context.Background())
	if err != nil || n.HTTP != "http://two:80" {
		t.Fatalf("cache expiry: %#v %v", n, err)
	}
	c = &nativeCache{read: func(ctx context.Context) (nativeConfig, error) { <-ctx.Done(); return nativeConfig{}, ctx.Err() }}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.get(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read cancellation: %v", err)
	}
}
