package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aemeath/agent/internal/link"
	"github.com/coder/websocket"
)

func pageStateService(t *testing.T) (*Service, *fakeConn) {
	t.Helper()
	s := testService(t, testDeps())
	c := &fakeConn{}
	s.mu.Lock()
	s.conn, s.connCtx, s.state.Connected = c, context.Background(), true
	s.mu.Unlock()
	return s, c
}

func assertDevicePage(t *testing.T, s *Service, page string, known bool) {
	t.Helper()
	state := s.Snapshot()
	if state.Page != page || state.PageKnown != known {
		t.Fatalf("page=%q known=%v, want %q/%v", state.Page, state.PageKnown, page, known)
	}
}

func TestPageStateReportsAreAuthoritative(t *testing.T) {
	s, conn := pageStateService(t)
	assertDevicePage(t, s, "", false)
	// Connect, swipe, temporary voice navigation and automatic restoration share
	// one report path. Receiving any of them must never echo a display command.
	for _, page := range []string{"quota", "metrics", "voice", "quota", "voice", "voice"} {
		s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"page_state","page":"`+page+`"}`))
		assertDevicePage(t, s, page, true)
		assertNoPageCommand(t, conn)
	}
	// A send does not imply acceptance, and a different actual report wins.
	if err := s.SetPage("metrics"); err != nil {
		t.Fatal(err)
	}
	if !conn.hasCommand("metrics") {
		t.Fatal("explicit command not sent")
	}
	assertDevicePage(t, s, "voice", true)
	s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"page_state","page":"quota"}`))
	assertDevicePage(t, s, "quota", true)
}

func TestPageStateInvalidAndStaleReportsIgnored(t *testing.T) {
	for _, known := range []bool{false, true} {
		s, conn := pageStateService(t)
		page := ""
		if known {
			page = "quota"
			s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"page_state","page":"quota"}`))
		}
		for _, invalid := range []string{
			`{`, `null`, `{}`,
			`{"v":2,"type":"page_state","page":"voice"}`,
			`{"v":1,"type":"display","page":"voice"}`,
			`{"v":1,"type":"page_state"}`,
			`{"v":1,"type":"page_state","page":null}`,
			`{"v":1,"type":"page_state","page":1}`,
			`{"v":1,"type":"page_state","page":""}`,
			`{"v":1,"type":"page_state","page":"unknown"}`,
			`{"v":1,"type":"page_state","page":"VOICE"}`,
		} {
			s.handleDeviceFrame(conn, []byte(invalid))
			assertDevicePage(t, s, page, known)
		}
		s.handleDeviceFrame(&fakeConn{}, []byte(`{"v":1,"type":"page_state","page":"voice"}`))
		s.handleDeviceFrame(nil, []byte(`{"v":1,"type":"page_state","page":"voice"}`))
		assertDevicePage(t, s, page, known)
		assertNoPageCommand(t, conn)
	}
}

func TestPageStateCancelledAttemptIgnored(t *testing.T) {
	s, conn := pageStateService(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.connCtx = ctx
	s.mu.Unlock()
	cancel()
	s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"page_state","page":"voice"}`))
	assertDevicePage(t, s, "", false)
}

func TestPageStateOverRealWebSocket(t *testing.T) {
	var commands atomic.Int32
	var metrics atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"v":1,"type":"page_state","page":"quota"}`)); err != nil {
			return
		}
		for {
			_, message, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(message, &frame) != nil {
				return
			}
			if frame["cpu"] != nil {
				metrics.Store(true)
			}
			if frame["type"] == "display" {
				commands.Add(1)
				// The actual page can differ from the request (e.g. a local swipe
				// or voice navigation). The device report must win.
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"v":1,"type":"page_state","page":"voice"}`)); err != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	deps := testDeps()
	deps.find = func(context.Context) (string, error) { return strings.TrimPrefix(server.URL, "http://"), nil }
	deps.dial = func(ctx context.Context, address string) (connection, error) {
		conn, err := link.Dial(ctx, address)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	s := testService(t, deps)
	defer s.Close()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Snapshot().PageKnown && s.Snapshot().Page == "quota" && metrics.Load() })
	if commands.Load() != 0 {
		t.Fatal("initial report triggered a display command")
	}
	if err := s.SetPage("metrics"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Snapshot().PageKnown && s.Snapshot().Page == "voice" })
	if commands.Load() != 1 {
		t.Fatalf("commands=%d, want one explicit request", commands.Load())
	}
}

func TestPageStateOldFirmwareAndDisconnect(t *testing.T) {
	s, conn := pageStateService(t)
	// Old firmware reports only power/brightness: page stays unknown and all
	// explicit navigation remains usable, without optimistic highlighting.
	s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"screen_state","on":false,"brightness":42}`))
	if state := s.Snapshot(); !state.ScreenKnown || state.ScreenOn || state.Brightness != 42 {
		t.Fatal("screen compatibility broken")
	}
	assertDevicePage(t, s, "", false)
	if err := s.SetPage("metrics"); err != nil {
		t.Fatal(err)
	}
	assertDevicePage(t, s, "", false)
	s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"page_state","page":"voice"}`))
	assertDevicePage(t, s, "voice", true)
	s.failConnection(conn, errors.New("test disconnect"))
	assertDevicePage(t, s, "", false)
	s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"page_state","page":"quota"}`))
	assertDevicePage(t, s, "", false)
	replacement := &fakeConn{}
	s.mu.Lock()
	s.conn, s.state.Connected = replacement, true
	s.mu.Unlock()
	s.handleDeviceFrame(conn, []byte(`{"v":1,"type":"page_state","page":"quota"}`))
	assertDevicePage(t, s, "", false)
	s.handleDeviceFrame(replacement, []byte(`{"v":1,"type":"page_state","page":"metrics"}`))
	assertDevicePage(t, s, "metrics", true)
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	assertDevicePage(t, s, "", false)
}
