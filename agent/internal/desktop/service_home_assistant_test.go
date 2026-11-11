package desktop

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/voice"
)

type fakeHAConnection struct {
	discover func(context.Context) ([]hamcp.Tool, error)
	closed   atomic.Bool
}

func (c *fakeHAConnection) Discover(ctx context.Context) ([]hamcp.Tool, error) {
	return c.discover(ctx)
}
func (c *fakeHAConnection) Close() error { c.closed.Store(true); return nil }

func TestHAConnectsAtStartupWithoutDeviceOrConversationAndRetries(t *testing.T) {
	deps := testDeps()
	deps.haEvery = 5 * time.Millisecond
	var calls atomic.Int32
	client := &fakeHAConnection{discover: func(context.Context) ([]hamcp.Tool, error) {
		if calls.Add(1) == 1 {
			return nil, hamcp.ErrUnavailable
		}
		return []hamcp.Tool{{Name: "HassTurnOn"}}, nil
	}}
	deps.haClient = func(hamcp.Config) (haConnection, error) { return client, nil }
	cfg, err := normalizeSettings(Settings{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Voice.HAMCPEnabled = true
	cfg.Voice.HAAccessToken = "fixture-token"
	s := newService("", cfg, deps)
	defer s.Close()
	eventually(t, func() bool { return s.Snapshot().Voice.HomeAssistant.Connected })
	state := s.Snapshot()
	if state.Running || state.Connected || state.Voice.HomeAssistant.ToolCount != 1 || calls.Load() < 2 {
		t.Fatal("HA must connect independently and recover", state)
	}
}

func TestHAConfigChangeCancelsProbeAndRejectsStaleResult(t *testing.T) {
	deps := testDeps()
	deps.haEvery = time.Hour
	started, canceled := make(chan struct{}), make(chan struct{})
	old := &fakeHAConnection{discover: func(ctx context.Context) ([]hamcp.Tool, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		// Even a misbehaving backend returning success after cancellation is stale.
		return []hamcp.Tool{{Name: "old"}}, nil
	}}
	next := &fakeHAConnection{discover: func(context.Context) ([]hamcp.Tool, error) { return []hamcp.Tool{{Name: "new"}, {Name: "new2"}}, nil }}
	deps.haClient = func(cfg hamcp.Config) (haConnection, error) {
		if cfg.Token == "old" {
			return old, nil
		}
		return next, nil
	}
	s := testService(t, deps)
	cfg := s.Snapshot().Settings
	cfg.Voice.HAMCPEnabled = true
	cfg.Voice.HAAccessToken = "old"
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	cfg.Voice.HAAccessToken = "new"
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("old probe not canceled")
	}
	eventually(t, func() bool { return s.Snapshot().Voice.HomeAssistant.ToolCount == 2 })
	if !old.closed.Load() {
		t.Fatal("old client leaked")
	}
	cfg.Voice.HAMCPEnabled = false
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return next.closed.Load() })
	if s.Snapshot().Voice.HomeAssistant != (hamcp.Status{}) {
		t.Fatal("disabled HA retained status")
	}
}

func TestHADisabledNeverConnectsAndCloseCancelsProbe(t *testing.T) {
	deps := testDeps()
	var calls atomic.Int32
	started := make(chan struct{})
	client := &fakeHAConnection{discover: func(ctx context.Context) ([]hamcp.Tool, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	deps.haClient = func(hamcp.Config) (haConnection, error) { calls.Add(1); return client, nil }
	s := testService(t, deps)
	if calls.Load() != 0 {
		t.Fatal("disabled HA connected")
	}
	cfg := s.Snapshot().Settings
	cfg.Voice.HAMCPEnabled = true
	cfg.Voice.HAAccessToken = "fixture-token"
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel HA")
	}
	if !client.closed.Load() {
		t.Fatal("HA client leaked")
	}
}

func TestHAVoiceUpdatesPreserveBackgroundStatus(t *testing.T) {
	deps := testDeps()
	states := make(chan voice.State)
	applied := make(chan struct{})
	deps.voiceRun = func(ctx context.Context, _ string, _ voice.Config, _ voice.Host, _ <-chan voice.Command, emit func(voice.State)) error {
		for {
			select {
			case state := <-states:
				emit(state)
				applied <- struct{}{}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	s := testService(t, deps)
	conn := &fakeConn{}
	connected := hamcp.Status{Connected: true, ToolCount: 3}
	s.mu.Lock()
	s.conn = conn
	s.state.Settings.Voice.Enabled = true
	s.state.Voice.HomeAssistant = connected
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(s.ctx)
	done := s.startVoice(ctx, conn, "device:80")
	defer func() { cancel(); <-done }()
	send := func(state voice.State) {
		t.Helper()
		select {
		case states <- state:
		case <-time.After(time.Second):
			t.Fatal("voice runner not ready")
		}
		select {
		case <-applied:
		case <-time.After(time.Second):
			t.Fatal("voice update not applied")
		}
	}
	send(voice.State{Phase: "idle"})
	if s.Snapshot().Voice.HomeAssistant != connected {
		t.Fatal("initial voice update erased startup HA status")
	}
	send(voice.State{Phase: "thinking", HomeAssistant: connected})
	failure := hamcp.Status{Error: "HA unavailable"}
	s.mu.Lock()
	s.state.Voice.HomeAssistant = failure
	s.mu.Unlock()
	send(voice.State{Phase: "idle", HomeAssistant: connected})
	if s.Snapshot().Voice.HomeAssistant != failure {
		t.Fatal("unchanged voice snapshot overwrote newer HA probe")
	}
	send(voice.State{Phase: "thinking", HomeAssistant: hamcp.Status{Connected: true, ToolCount: 4}})
	if s.Snapshot().Voice.HomeAssistant.ToolCount != 4 {
		t.Fatal("new conversation discovery was ignored")
	}
}
