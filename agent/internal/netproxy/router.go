package netproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var ErrClosed = errors.New("netproxy: router is closed")

// Router owns all of its pools. Updates atomically replace a generation; an
// already acquired request can finish, but no later request can acquire it.
// A Router must not be copied after first use. Construct it with New.
type Router struct {
	mu      sync.Mutex
	current *generation
	closed  bool
	native  *nativeCache
}

type generation struct {
	config     Config
	proxy      *url.URL
	transports map[*http.Transport]*http.Transport
	retired    bool // guarded by Router.mu
}

func New(c Config) (*Router, error) {
	c, err := Normalize(c)
	if err != nil {
		return nil, err
	}
	return &Router{current: newGeneration(c), native: newNativeCache()}, nil
}

func newGeneration(c Config) *generation {
	g := &generation{config: c, transports: make(map[*http.Transport]*http.Transport)}
	if c.Mode == ModeProxy {
		g.proxy, _ = parseProxy(c.ProxyURL, true)
	}
	return g
}

func (r *Router) Update(c Config) error {
	c, err := Normalize(c)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if r.current.config == c {
		return nil
	}
	old := r.current
	r.current = newGeneration(c)
	r.retire(old)
	return nil
}

// retire runs under the router lock. CloseIdleConnections also marks outstanding
// dials for closure. A second close on body release covers an active connection
// that becomes idle after retirement.
func (r *Router) retire(g *generation) {
	if g == nil {
		return
	}
	g.retired = true
	for _, tr := range g.transports {
		tr.CloseIdleConnections()
	}
}

func (r *Router) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.retire(r.current)
	}
}

type routerContextKey struct{}

func WithRouter(ctx context.Context, r *Router) context.Context {
	return context.WithValue(ctx, routerContextKey{}, r)
}

// This private template is stable even if callers replace http.DefaultTransport.
// Callers must treat custom templates as immutable after passing them to Transport.
var sharedTemplate = &http.Transport{
	DialContext:       (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
	TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
}
var systemRouter, _ = New(Config{Mode: ModeSystem})
var directRouter, _ = New(Config{Mode: ModeDirect})
var sharedScopedTransport = &scopedTransport{template: sharedTemplate}
var directScopedTransport = &scopedTransport{template: sharedTemplate, fixed: directRouter}

type scopedTransport struct {
	template *http.Transport
	fixed    *Router
}

// Transport wraps a template, not a pool. Construct it once per client, not once
// per request. The context selects a Router, or the shared system router if absent.
// Proxy on the template is intentionally overridden; other settings are cloned.
func Transport(base *http.Transport) http.RoundTripper {
	if base == nil {
		return sharedScopedTransport
	}
	return &scopedTransport{template: base}
}

// DirectTransport always ignores context routers, environment and native proxy
// settings. It has pools separate from the system router and explicit routers.
func DirectTransport() http.RoundTripper {
	return directScopedTransport
}

func (s *scopedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := s.fixed
	if r == nil {
		r, _ = req.Context().Value(routerContextKey{}).(*Router)
	}
	if r == nil {
		r = systemRouter
	}
	return r.roundTrip(req, s.template)
}

func closeRequest(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}

func (r *Router) roundTrip(req *http.Request, template *http.Transport) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		closeRequest(req)
		return nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		closeRequest(req)
		return nil, ErrClosed
	}
	g := r.current
	tr := g.transports[template]
	if tr == nil {
		tr = template.Clone()
		tr.Proxy = func(req *http.Request) (*url.URL, error) {
			if localHost(req.URL.Hostname()) {
				return nil, nil
			}
			switch g.config.Mode {
			case ModeDirect:
				return nil, nil
			case ModeProxy:
				return g.proxy, nil
			default:
				return r.systemProxy(req)
			}
		}
		g.transports[template] = tr
	}
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		if g.retired {
			tr.CloseIdleConnections()
		}
		r.mu.Unlock()
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		// net/http detaches dialing from the request context for pool reuse.
		// Once RoundTrip fails, close idle pools and cancel now-unwanted pending
		// dials as well (notably a SOCKS greeting that may otherwise wait forever).
		// This leaves active responses and dials with waiting requests untouched.
		tr.CloseIdleConnections()
		release()
		return nil, err
	}
	if resp.Body == nil {
		release()
		return resp, nil
	}
	b := &releaseBody{ReadCloser: resp.Body, release: release}
	if rw, ok := resp.Body.(io.ReadWriteCloser); ok {
		resp.Body = &releaseReadWriteBody{releaseBody: b, writer: rw}
	} else {
		resp.Body = b
	}
	return resp, nil
}

type releaseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *releaseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}
func (b *releaseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

type releaseReadWriteBody struct {
	*releaseBody
	writer io.Writer
}

func (b *releaseReadWriteBody) Write(p []byte) (int, error) { return b.writer.Write(p) }
