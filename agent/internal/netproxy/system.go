package netproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"
)

var errNativeUnsupported = errors.New("netproxy: native automatic proxy (PAC/WPAD) is unsupported; configure a manual proxy or direct mode")
var errNativeSettings = errors.New("netproxy: cannot read valid native proxy settings")

// nativeConfig is a small platform-independent snapshot. Endpoint values are
// URLs; native readers must validate enabled, but incomplete, manual settings.
type nativeConfig struct {
	HTTP, HTTPS, SOCKS string
	Bypass             []string
	Auto               bool
}

func (n nativeConfig) proxy(u *url.URL) (*url.URL, error) {
	if nativeBypass(u, n.Bypass) {
		return nil, nil
	}
	raw := ""
	switch u.Scheme {
	case "http":
		raw = n.HTTP
	case "https":
		raw = n.HTTPS
	}
	if raw == "" {
		raw = n.SOCKS
	}
	if raw == "" {
		if n.Auto {
			return nil, errNativeUnsupported
		}
		return nil, nil
	}
	p, err := parseProxy(raw, true)
	if err != nil {
		return nil, errNativeSettings
	}
	return p, nil
}

func (r *Router) systemProxy(req *http.Request) (*url.URL, error) {
	if localHost(req.URL.Hostname()) {
		return nil, nil
	}
	// Unlike net/http.ProxyFromEnvironment, this is intentionally not process-cached.
	env := httpproxy.FromEnvironment()
	raw := ""
	switch req.URL.Scheme {
	case "http":
		raw = env.HTTPProxy
	case "https":
		raw = env.HTTPSProxy
	}
	if raw != "" {
		check := raw
		if !strings.Contains(check, "://") {
			check = "http://" + check
		}
		if _, err := parseProxy(check, false); err != nil {
			return nil, errors.New("netproxy: invalid environment proxy settings")
		}
		p, err := env.ProxyFunc()(req.URL)
		if err != nil {
			return nil, errors.New("netproxy: environment proxy is unavailable")
		}
		return p, nil
	}
	// Apply the same NO_PROXY semantics to native settings, without treating absent
	// HTTP_PROXY as an instruction to ignore the OS proxy.
	bypass := &httpproxy.Config{HTTPProxy: "http://proxy.invalid:80", HTTPSProxy: "http://proxy.invalid:80", NoProxy: env.NoProxy}
	if p, _ := bypass.ProxyFunc()(req.URL); p == nil {
		return nil, nil
	}
	n, err := r.native.get(req.Context())
	if err != nil {
		return nil, err
	}
	return n.proxy(req.URL)
}

// nativeBypass supports WinINET <local>, wildcard hosts and optional ports, as
// well as macOS exception hostnames. Matching is entirely syntactic (no DNS).
func nativeBypass(u *url.URL, entries []string) bool {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	for _, entry := range entries {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if entry == "<local>" {
			if !strings.Contains(host, ".") && net.ParseIP(host) == nil {
				return true
			}
			continue
		}
		pattern := entry
		if h, p, err := net.SplitHostPort(entry); err == nil {
			if p != port && p != "*" {
				continue
			}
			pattern = h
		} else if strings.Count(entry, ":") == 1 {
			h, p, _ := strings.Cut(entry, ":")
			if p != port && p != "*" {
				continue
			}
			pattern = h
		}
		pattern = strings.TrimSuffix(strings.Trim(pattern, "[]"), ".")
		if host == pattern {
			return true
		}
		// path.Match provides just the required wildcard syntax, not regexp semantics.
		if ok, _ := path.Match(pattern, host); ok {
			return true
		}
		if strings.HasPrefix(pattern, ".") && strings.HasSuffix(host, pattern) {
			return true
		}
	}
	return false
}

// A per-router cache avoids global policy and stampedes. A waiting request can
// cancel independently. Both the reader and native process have a bounded lifetime.
type nativeCache struct {
	mu      sync.Mutex
	value   nativeConfig
	err     error
	expires time.Time
	loading chan struct{}
	read    func(context.Context) (nativeConfig, error)
}

func newNativeCache() *nativeCache { return &nativeCache{read: readNative} }
func (c *nativeCache) get(ctx context.Context) (nativeConfig, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nativeConfig{}, err
		}
		c.mu.Lock()
		if time.Now().Before(c.expires) {
			n, err := c.value, c.err
			c.mu.Unlock()
			return n, err
		}
		if pending := c.loading; pending != nil {
			c.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return nativeConfig{}, ctx.Err()
			}
		}
		c.loading = make(chan struct{})
		c.mu.Unlock()
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		n, err := c.read(bounded)
		cancel()
		c.mu.Lock()
		if ctx.Err() == nil {
			c.value, c.err, c.expires = n, err, time.Now().Add(5*time.Second)
		}
		close(c.loading)
		c.loading = nil
		c.mu.Unlock()
		if ctx.Err() != nil {
			return nativeConfig{}, ctx.Err()
		}
		return n, err
	}
}
