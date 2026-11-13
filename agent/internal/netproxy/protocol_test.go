package netproxy

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func forwardProxy(t *testing.T, origin *httptest.Server, secure bool, count *atomic.Int32) *httptest.Server {
	t.Helper()
	target, _ := url.Parse(origin.URL)
	upstream := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(upstream.CloseIdleConnections)
	reverse := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target); r.Out.Header.Del("Proxy-Authorization") }, Transport: upstream}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			t.Errorf("missing proxy authentication for %s", r.Method)
			http.Error(w, "auth", 407)
			return
		}
		if r.Method != http.MethodConnect {
			if !r.URL.IsAbs() || r.URL.Hostname() != "public.example" {
				t.Errorf("not an HTTP forward request: %s", r.RequestURI)
			}
			reverse.ServeHTTP(w, r)
			return
		}
		if r.Host != "public.example:443" {
			t.Errorf("unexpected CONNECT authority: %s", r.Host)
		}
		peer, err := net.DialTimeout("tcp", origin.Listener.Addr().String(), time.Second)
		if err != nil {
			http.Error(w, "upstream", 502)
			return
		}
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			peer.Close()
			return
		}
		defer conn.Close()
		defer peer.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		peer.SetDeadline(time.Now().Add(5 * time.Second))
		buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffered.Flush()
		go func() { io.Copy(peer, buffered); peer.Close() }()
		io.Copy(conn, peer)
	})
	var p *httptest.Server
	if secure {
		p = httptest.NewTLSServer(handler)
	} else {
		p = httptest.NewServer(handler)
	}
	t.Cleanup(p.Close)
	return p
}

func authenticated(raw string) string {
	u, _ := url.Parse(raw)
	u.User = url.UserPassword("user", "pass")
	return u.String()
}

func TestHTTPForwardAndCONNECT(t *testing.T) {
	for _, secureProxy := range []bool{false, true} {
		for _, secureOrigin := range []bool{false, true} {
			t.Run(fmt.Sprintf("proxyTLS=%t/originTLS=%t", secureProxy, secureOrigin), func(t *testing.T) {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Proxy-Authorization") != "" {
						t.Error("proxy credentials leaked to origin")
					}
					fmt.Fprintf(w, "origin:%s", r.URL.Path)
				})
				var origin *httptest.Server
				if secureOrigin {
					origin = httptest.NewTLSServer(handler)
				} else {
					origin = httptest.NewServer(handler)
				}
				defer origin.Close()
				var count atomic.Int32
				p := forwardProxy(t, origin, secureProxy, &count)
				r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: authenticated(p.URL)})
				template := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
				scheme := "http"
				if secureOrigin {
					scheme = "https"
				}
				if got := getText(t, Transport(template), r, scheme+"://public.example/path"); got != "origin:/path" {
					t.Fatal(got)
				}
				if count.Load() != 1 {
					t.Fatalf("expected one proxy request, got %d", count.Load())
				}
			})
		}
	}
}

func TestWebSocketUpgradeThroughProxySurvivesUpdate(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}))
	defer origin.Close()
	var count atomic.Int32
	p := forwardProxy(t, origin, false, &count)
	r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: authenticated(p.URL)})
	ctx, cancel := context.WithTimeout(WithRouter(context.Background(), r), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://public.example/socket", &websocket.DialOptions{HTTPClient: &http.Client{Transport: Transport(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := r.Update(Config{Mode: ModeDirect}); err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageText, []byte("alive")); err != nil {
		t.Fatal(err)
	}
	_, got, err := c.Read(ctx)
	if err != nil || string(got) != "alive" {
		t.Fatalf("upgraded stream lost writer/lifetime: %q %v", got, err)
	}
	if count.Load() != 1 {
		t.Fatal("websocket bypassed proxy")
	}
}

// socksProxy is a tiny local RFC1928/1929 server. It checks remote DNS framing
// and credentials before tunneling to a fixed local origin (never the internet).
func socksProxy(t *testing.T, origin string, auth bool, count *atomic.Int32) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer c.Close()
				c.SetDeadline(time.Now().Add(4 * time.Second))
				read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(c, b); return b, err }
				head, err := read(2)
				if err != nil {
					return
				}
				if head[0] != 5 {
					t.Error("bad SOCKS version")
					return
				}
				if _, err := read(int(head[1])); err != nil {
					return
				}
				method := byte(0)
				if auth {
					method = 2
				}
				c.Write([]byte{5, method})
				if auth {
					head, err = read(2)
					if err != nil {
						return
					}
					user, err := read(int(head[1]))
					if err != nil {
						return
					}
					size, err := read(1)
					if err != nil {
						return
					}
					pass, err := read(int(size[0]))
					if err != nil {
						return
					}
					if head[0] != 1 || string(user) != "user" || string(pass) != "pass" {
						t.Error("bad SOCKS credentials")
						return
					}
					c.Write([]byte{1, 0})
				}
				head, err = read(4)
				if err != nil {
					return
				}
				if head[0] != 5 || head[1] != 1 || head[3] != 3 {
					t.Errorf("SOCKS did not use domain CONNECT: %v", head)
					return
				}
				size, err := read(1)
				if err != nil {
					return
				}
				host, err := read(int(size[0]))
				if err != nil {
					return
				}
				port, err := read(2)
				if err != nil {
					return
				}
				if string(host) != "public.example" || binary.BigEndian.Uint16(port) != 80 {
					t.Errorf("bad SOCKS target: %s:%d", host, binary.BigEndian.Uint16(port))
					return
				}
				peer, err := net.DialTimeout("tcp", origin, time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				peer.SetDeadline(time.Now().Add(4 * time.Second))
				count.Add(1)
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
				copied := make(chan struct{})
				go func() { io.Copy(peer, c); peer.Close(); close(copied) }()
				io.Copy(c, peer)
				c.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done; workers.Wait() })
	endpoint := "socks5://" + ln.Addr().String()
	if auth {
		endpoint = authenticated(endpoint)
	}
	return endpoint
}

func TestSOCKS5Requests(t *testing.T) {
	for _, auth := range []bool{false, true} {
		t.Run(fmt.Sprintf("auth=%t", auth), func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "socks-origin") }))
			defer origin.Close()
			var count atomic.Int32
			endpoint := socksProxy(t, origin.Listener.Addr().String(), auth, &count)
			r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: endpoint})
			if got := getText(t, Transport(nil), r, "http://public.example/path"); got != "socks-origin" {
				t.Fatal(got)
			}
			if count.Load() != 1 {
				t.Fatal("SOCKS proxy bypassed")
			}
		})
	}
}

func TestRequestCancellationDuringProxyHandshake(t *testing.T) {
	accepted := make(chan struct{})
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(accepted); <-r.Context().Done() }))
	defer p.Close()
	r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: p.URL})
	ctx, cancel := context.WithCancel(WithRouter(context.Background(), r))
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://public.example", nil)
	done := make(chan error, 1)
	go func() { _, err := Transport(nil).RoundTrip(req); done <- err }()
	<-accepted
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy request did not cancel")
	}
}
