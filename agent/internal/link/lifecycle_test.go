package link

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type devicePeer struct {
	ready  chan error
	drop   chan struct{}
	done   chan struct{}
	page   string
	screen string
}

func floodDevice(t *testing.T) (string, <-chan *devicePeer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	peers := make(chan *devicePeer, 4)
	var handlers sync.WaitGroup
	var sequence atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.CloseNow()
		id := sequence.Add(1)
		peer := &devicePeer{
			ready: make(chan error, 1), drop: make(chan struct{}), done: make(chan struct{}),
			page:   fmt.Sprintf(`{"v":1,"type":"page_state","page":"voice","connection":%d}`, id),
			screen: fmt.Sprintf(`{"v":1,"type":"screen_state","on":false,"brightness":42,"connection":%d}`, id),
		}
		defer close(peer.done)
		peers <- peer
		// Ping needs an active server-side reader to process the client's pong.
		reading := ws.CloseRead(ctx)
		defer func() {
			ws.CloseNow()
			<-reading.Done()
		}()
		write := func(kind websocket.MessageType, data string) bool {
			if err := ws.Write(ctx, kind, []byte(data)); err != nil {
				peer.ready <- err
				return false
			}
			return true
		}
		for i := 0; i < 128; i++ {
			for _, data := range []string{
				`{"type":"unknown"}`,
				`{"v":1,"type":"page_state","page":"metrics"}`,
				`{"v":1,"type":"screen_state","on":true,"brightness":99}`,
			} {
				if !write(websocket.MessageText, data) {
					return
				}
			}
		}
		if !write(websocket.MessageText, peer.page) || !write(websocket.MessageText, peer.screen) {
			return
		}
		for i := 0; i < 64; i++ {
			if !write(websocket.MessageText, `{"type":"unknown"}`) ||
				!write(websocket.MessageText, `{"v":2,"type":"page_state","page":"quota"}`) ||
				!write(websocket.MessageText, `{"v":1,"type":"screen_state","on":true}`) {
				return
			}
		}
		if !write(websocket.MessageBinary, `{"v":1,"type":"page_state","page":"quota"}`) {
			return
		}
		// The pong is a deterministic barrier: all prior frames were read and
		// enqueued even though the client has never consumed Incoming.
		peer.ready <- ws.Ping(ctx)
		select {
		case <-peer.drop:
		case <-reading.Done():
		case <-ctx.Done():
		}
	}))
	t.Cleanup(func() {
		cancel()
		server.Close()
		handlers.Wait()
	})
	return strings.TrimPrefix(server.URL, "http://"), peers
}

func nextPeer(t *testing.T, peers <-chan *devicePeer) *devicePeer {
	t.Helper()
	select {
	case peer := <-peers:
		select {
		case err := <-peer.ready:
			if err != nil {
				t.Fatalf("device flood/ping barrier: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WS reader blocked while Incoming had no consumer")
		}
		return peer
	case <-time.After(5 * time.Second):
		t.Fatal("device connection was not accepted")
		return nil
	}
}

func assertConnStopped(t *testing.T, conn *Conn) {
	t.Helper()
	waitDone(t, conn.readDone)
	waitDone(t, conn.inbox.stopped)
	if _, ok := <-conn.Incoming(); ok {
		t.Fatal("Incoming did not close with connection")
	}
}

func TestWebSocketLatestStatesAfterUnreadFlood(t *testing.T) {
	addr, peers := floodDevice(t)
	conn, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	peer := nextPeer(t, peers)
	receiveLatest(t, conn.Incoming(), peer.page, peer.screen)
	conn.Close()
	assertConnStopped(t, conn)
	waitDone(t, peer.done)
}

func TestWebSocketCloseAbortsUnreadDispatcher(t *testing.T) {
	addr, peers := floodDevice(t)
	conn, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	peer := nextPeer(t, peers)
	// Never receive Incoming until Close has joined both client goroutines.
	closed := make(chan struct{})
	go func() { defer close(closed); conn.Close() }()
	waitDone(t, closed)
	assertConnStopped(t, conn)
	waitDone(t, peer.done)
}

func TestWebSocketDisconnectAndReconnectDiscardOldPending(t *testing.T) {
	addr, peers := floodDevice(t)
	var previousPage string
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := Dial(context.Background(), addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conn.Close)
		peer := nextPeer(t, peers)
		if attempt == 1 {
			if previousPage == peer.page {
				t.Fatal("fixture did not establish a new connection")
			}
			receiveLatest(t, conn.Incoming(), peer.page, peer.screen)
		}
		previousPage = peer.page
		close(peer.drop) // abrupt remote disconnect, including with no consumer
		waitDone(t, peer.done)
		assertConnStopped(t, conn)
		conn.Close()
	}
}
