package netproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSCUtilIgnoresScopedDictionaries(t *testing.T) {
	for _, global := range []string{"", "HTTPEnable : 1\n HTTPProxy : global.example\n HTTPPort : 8080\n"} {
		raw := "<dictionary> {\n" + global + " __SCOPED__ : <dictionary> {\n en0 : <dictionary> {\n HTTPEnable : 1\n HTTPProxy : scoped.example\n HTTPPort : 9999\n ExceptionsList : <array> {\n 0 : *\n }\n }\n }\n}\n"
		n, err := parseSCUtil(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(n.Bypass) != 0 || strings.Contains(n.HTTP, "scoped") {
			t.Fatalf("scoped settings overwrote global: %#v", n)
		}
		if global != "" && n.HTTP != "http://global.example:8080" {
			t.Fatalf("global settings lost: %#v", n)
		}
	}
}

func TestUnchangedUpdateRetainsPool(t *testing.T) {
	p := labelProxy(t, "proxy")
	c := Config{Mode: ModeProxy, ProxyURL: p.URL}
	r := mustRouter(t, c)
	rt := Transport(nil)
	before := getText(t, rt, r, "http://public.example")
	if err := r.Update(c); err != nil {
		t.Fatal(err)
	}
	if after := getText(t, rt, r, "http://public.example"); before != after {
		t.Fatal("unchanged update discarded pool")
	}
}

type trackedBody struct{ closed atomic.Bool }

func (*trackedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *trackedBody) Close() error           { b.closed.Store(true); return nil }

func TestEarlyErrorsCloseRequestBodies(t *testing.T) {
	r := mustRouter(t, Config{Mode: ModeDirect})
	r.Close()
	for _, canceled := range []bool{false, true} {
		ctx := WithRouter(context.Background(), r)
		if canceled {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		body := &trackedBody{}
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://public.example", body)
		_, err := Transport(nil).RoundTrip(req)
		if err == nil || !body.closed.Load() {
			t.Fatalf("early error left body open: %v", err)
		}
	}
}

func TestNativeCacheWaiterCancelsDuringRead(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	c := &nativeCache{read: func(ctx context.Context) (nativeConfig, error) {
		close(started)
		select {
		case <-finish:
			return nativeConfig{}, nil
		case <-ctx.Done():
			return nativeConfig{}, ctx.Err()
		}
	}}
	done := make(chan struct{})
	go func() { defer close(done); c.get(context.Background()) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.get(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter blocked past cancellation: %v", err)
	}
	close(finish)
	<-done
}
