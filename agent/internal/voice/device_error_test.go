package voice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func errorTestSession(t *testing.T, turn uint32) (*session, testDevice) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		accepted <- ws
		<-ctx.Done()
	}))
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatal(err)
	}
	device := <-accepted
	t.Cleanup(func() { cancel(); ws.CloseNow(); server.Close() })
	s := &session{ctx: ctx, ws: ws, epoch: 42, state: State{Turn: turn, Phase: "listening", Notice: "old guidance"}, wireTurn: turn, firstWireTurn: turn, emit: func(State) {}}
	return s, testDevice{ctx: ctx, ws: device, epoch: 42}
}

func readErrorTestMessage(t *testing.T, d testDevice) message {
	t.Helper()
	typ, b, err := d.read()
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("unexpected binary message: %x", b)
	}
	m, err := decodeMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDeviceErrorDoesNotEchoAndPreservesOriginalFailure(t *testing.T) {
	for _, turn := range []uint32{0, 7} {
		t.Run(fmt.Sprint(turn), func(t *testing.T) {
			s, d := errorTestSession(t, turn)
			s.active = turn != 0
			s.state.WakeReady = true
			s.cfg.MiMoAPIKey = "secret-key"
			canceled := false
			s.cancelTurn = func() { canceled = true }
			s.capture = &capture{}
			s.play = &playback{}
			var snapshot State
			s.emit = func(state State) { snapshot = state }
			recordControl(t, s, message{Type: "error", Turn: turn, Code: "audio_unavailable", Message: "Audio hardware initialization failed secret-key"})
			if snapshot.Phase != "error" || snapshot.Error != "device voice error: Audio hardware initialization failed [redacted]" || snapshot.Notice != "" || snapshot.WakeReady {
				t.Fatalf("original failure not preserved: %+v", snapshot)
			}
			if !canceled || s.active || s.capture != nil || s.play != nil || !s.deadline.IsZero() {
				t.Fatal("device failure left local work or playback running")
			}
			// A sentinel must be the next frame: no cancel/error/state echo may precede it.
			if err := s.phase("idle", "sentinel"); err != nil {
				t.Fatal(err)
			}
			m := readErrorTestMessage(t, d)
			if m.Type != "state" || m.State != "idle" || m.Text != "sentinel" {
				t.Fatalf("device error was echoed: %+v", m)
			}
		})
	}
}

func TestLocalFailureBeforeFirstTurnDoesNotSendInvalidState(t *testing.T) {
	s, d := errorTestSession(t, 0)
	if err := s.fail("bad_audio", errors.New("invalid microphone frame")); err != nil {
		t.Fatal(err)
	}
	m := readErrorTestMessage(t, d)
	if m.Type != "error" || m.Turn != 0 || m.Message != "invalid microphone frame" || s.state.Phase != "error" {
		t.Fatalf("missing connection-scope failure: %+v state=%+v", m, s.state)
	}
	if err := s.phase("idle", "sentinel"); err != nil {
		t.Fatal(err)
	}
	m = readErrorTestMessage(t, d)
	if m.Type != "state" || m.State != "idle" || m.Text != "sentinel" {
		t.Fatalf("sent state:error at turn 0 to old firmware: %+v", m)
	}
}

func TestLocalRoundFailureStillNotifiesDevice(t *testing.T) {
	s, d := errorTestSession(t, 7)
	s.active = true
	if err := s.fail("thinking", errors.New("model unavailable")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cancel", "error", "state"} {
		m := readErrorTestMessage(t, d)
		if m.Type != want || m.Turn != 7 || (want == "state" && m.State != "error") {
			t.Fatalf("local failure notification: got %+v, want %s turn 7", m, want)
		}
	}
}
