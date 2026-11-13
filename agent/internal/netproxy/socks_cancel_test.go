package netproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanceledSOCKSHandshakeClosesDetachedDial(t *testing.T) {
	testCanceledProxyHandshake(t, "socks5")
}

func TestCanceledCONNECTHandshakeClosesDetachedDial(t *testing.T) {
	testCanceledProxyHandshake(t, "http")
}

func testCanceledProxyHandshake(t *testing.T, proxyScheme string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var peers sync.WaitGroup
	var accepted atomic.Int32
	greeting := make(chan struct{}, 1)
	peerClosed := make(chan error, 1)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			peers.Add(1)
			go func() {
				defer peers.Done()
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				if proxyScheme == "socks5" {
					head := make([]byte, 2)
					if _, err := io.ReadFull(c, head); err != nil {
						peerClosed <- err
						return
					}
					methods := make([]byte, int(head[1]))
					if _, err := io.ReadFull(c, methods); err != nil {
						peerClosed <- err
						return
					}
				} else {
					req, err := http.ReadRequest(bufio.NewReader(c))
					if err != nil {
						peerClosed <- err
						return
					}
					req.Body.Close()
					if req.Method != http.MethodConnect {
						t.Errorf("expected CONNECT, got %s", req.Method)
					}
				}
				greeting <- struct{}{}
				// Accept the greeting but deliberately never send a SOCKS method response.
				var one [1]byte
				_, err := c.Read(one[:])
				peerClosed <- err
			}()
		}
	}()
	defer func() { ln.Close(); <-acceptDone; peers.Wait() }()
	r := mustRouter(t, Config{Mode: ModeProxy, ProxyURL: proxyScheme + "://" + ln.Addr().String()})
	var directDials atomic.Int32
	template := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != ln.Addr().String() {
			directDials.Add(1)
			return nil, errors.New("test forbids direct fallback")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	ctx, cancel := context.WithCancel(WithRouter(context.Background(), r))
	defer cancel()
	target := "http://public.example"
	if proxyScheme == "http" {
		target = "https://public.example"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
	returned := make(chan error, 1)
	go func() { _, err := Transport(template).RoundTrip(req); returned <- err }()
	select {
	case <-greeting:
	case <-time.After(time.Second):
		t.Fatal("SOCKS greeting was not sent")
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request did not return")
	}
	// Assert EOF before Router.Close/Update. Returning from RoundTrip alone does not
	// prove that net/http's detached SOCKS dial and peer socket have been cleaned up.
	select {
	case err := <-peerClosed:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("SOCKS peer did not see EOF: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("canceled SOCKS handshake left detached dial socket open")
	}
	if directDials.Load() != 0 {
		t.Fatal("canceled proxy dial attempted direct fallback")
	}
	if accepted.Load() != 1 {
		t.Fatalf("canceled SOCKS dial retried %d connections", accepted.Load())
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		t.Fatal("test closed router before checking socket cleanup")
	}
}

func TestConcurrentRealGenerationUpdates(t *testing.T) {
	arrived, finish := make(chan struct{}), make(chan struct{})
	var arrivedOnce, finishOnce sync.Once
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrivedOnce.Do(func() { close(arrived) })
		<-finish
		w.Write([]byte("origin"))
	}))
	t.Cleanup(origin.Close)
	defer finishOnce.Do(func() { close(finish) })
	r := mustRouter(t, Config{Mode: ModeDirect})
	rt := Transport(nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 20; j++ {
				ctx, cancel := context.WithTimeout(WithRouter(context.Background(), r), time.Second)
				req, _ := http.NewRequestWithContext(ctx, "GET", origin.URL, nil)
				resp, err := rt.RoundTrip(req)
				if err == nil {
					_, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				cancel()
				if err != nil {
					t.Errorf("request during real generation update: %v", err)
					return
				}
			}
		}()
	}
	close(start)
	// Ensure there is a live response when the first generation is retired.
	<-arrived
	for i := 0; i < 40; i++ {
		mode := ModeSystem
		if i%2 != 0 {
			mode = ModeDirect
		}
		r.mu.Lock()
		before := r.current
		r.mu.Unlock()
		if err := r.Update(Config{Mode: mode}); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		changed := before != r.current && before.retired
		r.mu.Unlock()
		if !changed {
			t.Fatal("update did not actually retire a generation")
		}
	}
	finishOnce.Do(func() { close(finish) })
	wg.Wait()
}

func TestCanceledDialCleanupPreservesAnotherPendingDial(t *testing.T) {
	origin := labelProxy(t, "origin")
	r := mustRouter(t, Config{Mode: ModeDirect})
	template := localTemplate(origin.Listener.Addr().String())
	dialOrigin := template.DialContext
	firstStarted, secondStarted, allowSecond := make(chan struct{}), make(chan struct{}), make(chan struct{})
	template.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "first.example:80" {
			close(firstStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		close(secondStarted)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-allowSecond:
			return dialOrigin(ctx, network, addr)
		}
	}
	rt := Transport(template)
	firstCtx, cancelFirst := context.WithCancel(WithRouter(context.Background(), r))
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithTimeout(WithRouter(context.Background(), r), 2*time.Second)
	defer cancelSecond()
	first, _ := http.NewRequestWithContext(firstCtx, "GET", "http://first.example", nil)
	second, _ := http.NewRequestWithContext(secondCtx, "GET", "http://second.example", nil)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := rt.RoundTrip(first); firstDone <- err }()
	<-firstStarted
	go func() {
		resp, err := rt.RoundTrip(second)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		secondDone <- err
	}()
	<-secondStarted
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first request: %v", err)
	}
	close(allowSecond)
	if err := <-secondDone; err != nil {
		t.Fatalf("cleanup canceled an unrelated waiting dial: %v", err)
	}
}

func TestCanceledDialCleanupDoesNotInterruptActiveResponse(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		close(started)
		<-finish
		w.Write([]byte("last"))
	}))
	defer origin.Close()
	r := mustRouter(t, Config{Mode: ModeDirect})
	template := localTemplate(origin.Listener.Addr().String())
	originalDial := template.DialContext
	dialing := make(chan struct{})
	template.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "cancel.example:80" {
			close(dialing)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return originalDial(ctx, network, addr)
	}
	rt := Transport(template)
	resp, err := request(t, rt, r, "http://active.example")
	if err != nil {
		close(finish)
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	first := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		close(finish)
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(WithRouter(context.Background(), r))
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://cancel.example", nil)
	done := make(chan error, 1)
	go func() { _, err := rt.RoundTrip(req); done <- err }()
	<-dialing
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		close(finish)
		t.Fatalf("cancel: %v", err)
	}
	close(finish)
	rest, err := io.ReadAll(resp.Body)
	if err != nil || string(first)+string(rest) != "firstlast" {
		t.Fatalf("active response interrupted: %q %v", rest, err)
	}
}
