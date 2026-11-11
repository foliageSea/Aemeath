package desktop

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/proto"
	"aemeath/agent/internal/voice"
)

type cancelControlConn struct{ entered chan struct{} }

func (c *cancelControlConn) Send(ctx context.Context, _ any) error {
	close(c.entered)
	<-ctx.Done()
	return ctx.Err()
}
func (c *cancelControlConn) Incoming() <-chan []byte { return nil }
func (c *cancelControlConn) Close()                  {}

func TestCanceledVoiceToolDoesNotDropTelemetry(t *testing.T) {
	for _, kind := range []string{"page", "screen"} {
		t.Run(kind, func(t *testing.T) {
			s := testService(t, testDeps())
			c := &cancelControlConn{entered: make(chan struct{})}
			s.mu.Lock()
			s.conn = c
			s.connCtx = s.ctx
			s.state.Connected = true
			s.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if kind == "page" {
					done <- (voiceHost{s}).SetPage(ctx, "voice")
				} else {
					done <- (voiceHost{s}).SetScreen(ctx, false, 20)
				}
			}()
			select {
			case <-c.entered:
			case <-time.After(time.Second):
				t.Fatal("control was not sent")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("tool ignored cancellation")
			}
			if !s.Snapshot().Connected {
				t.Fatal("canceling one tool dropped telemetry link")
			}
		})
	}
}

func TestVoiceCommandTextValidation(t *testing.T) {
	s := testService(t, testDeps())
	s.mu.Lock()
	s.state.Settings.Voice.Enabled = true
	s.state.Voice.Connected = true
	s.voiceCommands = make(chan voice.Command, 8)
	s.mu.Unlock()
	for _, text := range []string{"", " \n\t", "\u3000", strings.Repeat("a", 4097), strings.Repeat("界", 1366), " " + strings.Repeat("a", 4096)} {
		if err := s.VoiceCommand("text", text); err == nil {
			t.Fatalf("accepted invalid input of %d bytes", len(text))
		}
		if len(s.voiceCommands) != 0 {
			t.Fatal("invalid text was queued")
		}
	}
	for _, text := range []string{" \n 你好 \t", strings.Repeat("a", 4096), strings.Repeat("界", 1365) + "a"} {
		if err := s.VoiceCommand("text", text); err != nil {
			t.Fatal(err)
		}
		if got := <-s.voiceCommands; got.Kind != "text" || got.Text != strings.TrimSpace(text) {
			t.Fatalf("queued %+v", got)
		}
	}
	for i := 0; i < cap(s.voiceCommands); i++ {
		s.voiceCommands <- voice.Command{Kind: "cancel"}
	}
	if err := s.VoiceCommand("text", "hello"); err == nil {
		t.Fatal("accepted command with full queue")
	}
}

func TestVoiceTextCommandsRespectServiceAvailability(t *testing.T) {
	s := testService(t, testDeps())
	if err := s.VoiceCommand("text", "hello"); err == nil {
		t.Fatal("accepted while disabled")
	}
	s.mu.Lock()
	s.state.Settings.Voice.Enabled = true
	s.mu.Unlock()
	if err := s.VoiceCommand("text", "hello"); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline: %v", err)
	}
	s.mu.Lock()
	s.state.Voice.Connected = true
	s.mu.Unlock()
	if err := s.VoiceCommand("text", "hello"); !errors.Is(err, ErrOffline) {
		t.Fatalf("missing queue: %v", err)
	}
	s.Close()
	if err := s.VoiceCommand("text", "hello"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func TestVoiceCommandRecordingAllowlist(t *testing.T) {
	s := testService(t, testDeps())
	s.mu.Lock()
	s.state.Settings.Voice.Enabled = true
	s.state.Voice.Connected = true
	s.voiceCommands = make(chan voice.Command, 8)
	s.mu.Unlock()
	for _, kind := range []string{"record_start", "record_stop", "record_cancel"} {
		if err := s.VoiceCommand(kind, ""); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got := <-s.voiceCommands; got.Kind != kind || got.Text != "" {
			t.Fatalf("queued %+v", got)
		}
	}
	for _, kind := range []string{"record", "record_end", "Record_Start", ""} {
		if err := s.VoiceCommand(kind, ""); err == nil {
			t.Fatalf("accepted unknown command %q", kind)
		}
	}
	if len(s.voiceCommands) != 0 {
		t.Fatal("unknown command was queued")
	}
}

func TestVoiceRecordingCommandsRespectServiceAvailability(t *testing.T) {
	for _, kind := range []string{"record_start", "record_stop", "record_cancel"} {
		t.Run(kind, func(t *testing.T) {
			s := testService(t, testDeps())
			if err := s.VoiceCommand(kind, ""); err == nil {
				t.Fatal("accepted while disabled")
			}
			s.mu.Lock()
			s.state.Settings.Voice.Enabled = true
			s.mu.Unlock()
			if err := s.VoiceCommand(kind, ""); !errors.Is(err, ErrOffline) {
				t.Fatalf("offline: %v", err)
			}
			s.mu.Lock()
			s.state.Voice.Connected = true
			s.voiceCommands = make(chan voice.Command, 1)
			s.voiceCommands <- voice.Command{Kind: "wake"}
			s.mu.Unlock()
			if err := s.VoiceCommand(kind, ""); err == nil {
				t.Fatal("accepted with full queue")
			}
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			if err := s.VoiceCommand(kind, ""); !errors.Is(err, ErrClosed) {
				t.Fatalf("closed: %v", err)
			}
			s.mu.Lock()
			s.closed = false
			s.mu.Unlock()
		})
	}
}

func enableTestVoice(t *testing.T, s *Service) {
	t.Helper()
	settings := s.Snapshot().Settings
	settings.Voice.Enabled = true
	settings.Voice.Volume = 70
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceLifecycleCommandsAndIndependentSampling(t *testing.T) {
	deps := testDeps()
	var samples, active atomic.Int32
	deps.sample = func(context.Context) proto.Frame { samples.Add(1); return proto.Frame{V: 1} }
	received := make(chan voice.Command, 1)
	deps.voiceRun = func(ctx context.Context, _ string, _ voice.Config, _ voice.Host, commands <-chan voice.Command, emit func(voice.State)) error {
		active.Add(1)
		defer active.Add(-1)
		emit(voice.State{Connected: true, WakeReady: true, Phase: "listening"})
		select {
		case cmd := <-commands:
			received <- cmd
		case <-ctx.Done():
			return ctx.Err()
		}
		<-ctx.Done()
		// An expired worker is not allowed to overwrite the next service state.
		emit(voice.State{Connected: true, Phase: "speaking", Response: "stale"})
		return ctx.Err()
	}
	s := testService(t, deps)
	enableTestVoice(t, s)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Snapshot().Voice.WakeReady })
	before := samples.Load()
	eventually(t, func() bool { return samples.Load() > before+2 })
	if err := s.VoiceCommand("preview", "你好"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.Kind != "preview" || got.Text != "你好" {
			t.Fatalf("command %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("command was not delivered")
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if active.Load() != 0 {
		t.Fatal("Stop returned before audio worker exited")
	}
	state := s.Snapshot().Voice
	if state.Connected || state.WakeReady || state.Response == "stale" {
		t.Fatalf("stale worker state escaped: %#v", state)
	}
	if err := s.VoiceCommand("wake", ""); !errors.Is(err, ErrOffline) {
		t.Fatalf("stopped command error: %v", err)
	}
}

func TestVoiceConfigurationRestartJoinsPreviousSession(t *testing.T) {
	deps := testDeps()
	calls := make(chan voice.Config, 4)
	var active, overlap atomic.Int32
	deps.voiceRun = func(ctx context.Context, _ string, cfg voice.Config, _ voice.Host, _ <-chan voice.Command, emit func(voice.State)) error {
		if active.Add(1) > 1 {
			overlap.Add(1)
		}
		defer active.Add(-1)
		calls <- cfg
		emit(voice.State{Connected: true, Phase: "idle"})
		<-ctx.Done()
		return ctx.Err()
	}
	s := testService(t, deps)
	enableTestVoice(t, s)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("initial voice session missing")
	}
	cfg := s.Snapshot().Settings
	cfg.Voice.WakePhrase = "小智同学"
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-calls:
		if got.WakePhrase != cfg.Voice.WakePhrase {
			t.Fatal("reconnect used old wake phrase")
		}
		stored, err := loadSettings(s.path)
		if err != nil || stored.Voice.WakePhrase != cfg.Voice.WakePhrase {
			t.Fatalf("wake phrase not persisted: %+v, %v", stored.Voice, err)
		}
	case <-time.After(time.Second):
		t.Fatal("settings did not restart voice session")
	}
	if overlap.Load() != 0 {
		t.Fatal("two audio sessions overlapped")
	}
	cfg.Voice.Enabled = false
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return active.Load() == 0 && s.Snapshot().Voice.Phase == "disabled" })
	if err := s.VoiceCommand("wake", ""); err == nil {
		t.Fatal("disabled assistant accepted wake")
	}
}

func TestOrdinaryDisconnectRetainsRetryBackoff(t *testing.T) {
	deps := testDeps()
	deps.retryEvery = 150 * time.Millisecond
	calls := make(chan struct{}, 16)
	deps.dial = func(context.Context, string) (connection, error) {
		ch := make(chan []byte)
		close(ch)
		calls <- struct{}{}
		return &fakeConn{incoming: ch}, nil
	}
	s := testService(t, deps)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("no initial dial")
	}
	select {
	case <-calls:
		t.Fatal("ordinary disconnect skipped retry backoff")
	case <-time.After(40 * time.Millisecond):
	}
}

func TestVoiceCommandsRejectInvalidAndFullQueue(t *testing.T) {
	s := testService(t, testDeps())
	if err := s.VoiceCommand("unknown", ""); err == nil {
		t.Fatal("unknown command accepted")
	}
	s.mu.Lock()
	s.state.Settings.Voice.Enabled = true
	s.state.Voice.Connected = true
	s.voiceCommands = make(chan voice.Command, 1)
	s.mu.Unlock()
	if err := s.VoiceCommand("clear", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.VoiceCommand("wake", ""); err == nil {
		t.Fatal("full queue accepted a command")
	}
}
