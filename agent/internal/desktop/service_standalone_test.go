package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/voice"
)

func TestRunModeSettingsMigrationAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"address":"legacy.local","voice":{"playbackOutput":"agent"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadSettings(path)
	if err != nil || settings.RunMode != RunModeDevice {
		t.Fatalf("legacy migration: %+v, %v", settings, err)
	}
	for _, input := range []string{"", "device", " DEVICE ", "standalone", " Standalone "} {
		got, err := normalizeSettings(Settings{RunMode: input})
		if err != nil {
			t.Fatal(err)
		}
		want := RunModeDevice
		if strings.EqualFold(strings.TrimSpace(input), RunModeStandalone) {
			want = RunModeStandalone
		}
		if got.RunMode != want {
			t.Fatalf("%q: mode %q", input, got.RunMode)
		}
	}
	if _, err := normalizeSettings(Settings{RunMode: "automatic"}); err == nil {
		t.Fatal("accepted unknown mode")
	}
	settings.RunMode = RunModeStandalone
	if err := persistSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	saved, err := loadSettings(path)
	if err != nil || saved.RunMode != RunModeStandalone || saved.Voice.PlaybackOutput != "agent" {
		t.Fatalf("round trip: %+v, %v", saved, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"runMode": "standalone"`) {
		t.Fatalf("missing persisted mode: %s, %v", data, err)
	}
}

type standaloneCall struct {
	cfg      voice.Config
	emit     func(voice.State)
	commands <-chan voice.Command
}

func receiveStandaloneCall(t *testing.T, calls <-chan standaloneCall) standaloneCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("voice worker did not start")
		return standaloneCall{}
	}
}

func TestStandaloneServiceNoDeviceIOAndLifecycle(t *testing.T) {
	deps := testDeps()
	var finds, dials, active atomic.Int32
	deps.find = func(context.Context) (string, error) { finds.Add(1); return "", errors.New("unexpected discovery") }
	deps.dial = func(context.Context, string) (connection, error) {
		dials.Add(1)
		return nil, errors.New("unexpected dial")
	}
	calls := make(chan standaloneCall, 8)
	deps.voiceRunStandalone = func(ctx context.Context, cfg voice.Config, host voice.Host, commands <-chan voice.Command, emit func(voice.State)) error {
		active.Add(1)
		defer active.Add(-1)
		emit(voice.State{Connected: true, Phase: "idle"})
		calls <- standaloneCall{cfg, emit, commands}
		<-ctx.Done()
		return ctx.Err()
	}
	s := testService(t, deps)
	settings := s.Snapshot().Settings
	settings.RunMode, settings.Voice.Enabled, settings.Voice.PlaybackOutput = RunModeStandalone, true, "agent"
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	first := receiveStandaloneCall(t, calls)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	state := s.Snapshot()
	if !state.Running || state.Connected || !state.Voice.Connected || state.Target != "" || state.Status != "standalone" {
		t.Fatalf("standalone state: %+v", state)
	}
	if first.cfg.PlaybackOutput != "agent" || state.Settings.Voice.PlaybackOutput != "agent" {
		t.Fatal("service rewrote saved playback preference")
	}
	if err := s.VoiceCommand("text", "hello"); err != nil {
		t.Fatal(err)
	}
	select {
	case command := <-first.commands:
		if command.Kind != "text" {
			t.Fatal(command)
		}
	case <-time.After(time.Second):
		t.Fatal("voice command lost")
	}
	if err := s.SetPage("voice"); !errors.Is(err, ErrOffline) {
		t.Fatalf("device control: %v", err)
	}
	settings.Voice.Persona = "changed"
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	second := receiveStandaloneCall(t, calls)
	if second.cfg.Persona != "changed" || active.Load() != 1 {
		t.Fatal("settings did not restart joined standalone worker")
	}
	first.emit(voice.State{Connected: true, Response: "stale settings callback"})
	if s.Snapshot().Voice.Response != "" {
		t.Fatal("stale callback replaced current session")
	}
	if err := s.ReloadWakeModels(s.Snapshot().Settings.Voice.WakeModelDir); err != nil {
		t.Fatal(err)
	}
	third := receiveStandaloneCall(t, calls)
	second.emit(voice.State{Connected: true, Response: "stale model callback"})
	if s.Snapshot().Voice.Response != "" {
		t.Fatal("reload accepted stale callback")
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	third.emit(voice.State{Connected: true, Response: "after stop"})
	if s.Snapshot().Running || s.Snapshot().Voice.Connected || active.Load() != 0 {
		t.Fatal("stop did not cancel and join standalone worker")
	}
	if finds.Load() != 0 || dials.Load() != 0 {
		t.Fatalf("standalone device IO: discovery=%d dial=%d", finds.Load(), dials.Load())
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	fourth := receiveStandaloneCall(t, calls)
	s.Close()
	fourth.emit(voice.State{Connected: true, Response: "after close"})
	if active.Load() != 0 || s.Snapshot().Voice.Connected {
		t.Fatal("close did not join standalone worker")
	}
}

func TestModeSwitchJoinsStandaloneBeforeDeviceStarts(t *testing.T) {
	deps := testDeps()
	calls := make(chan standaloneCall, 2)
	canceled, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	deps.voiceRunStandalone = func(ctx context.Context, cfg voice.Config, _ voice.Host, commands <-chan voice.Command, emit func(voice.State)) error {
		emit(voice.State{Connected: true})
		calls <- standaloneCall{cfg, emit, commands}
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}
	device := make(chan func(voice.State), 1)
	deps.voiceRun = func(ctx context.Context, _ string, _ voice.Config, _ voice.Host, _ <-chan voice.Command, emit func(voice.State)) error {
		emit(voice.State{Connected: true, Phase: "idle"})
		device <- emit
		<-ctx.Done()
		return ctx.Err()
	}
	var dials atomic.Int32
	deps.dial = func(context.Context, string) (connection, error) { dials.Add(1); return &fakeConn{}, nil }
	s := testService(t, deps)
	settings := s.Snapshot().Settings
	settings.RunMode, settings.Voice.Enabled = RunModeStandalone, true
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	old := receiveStandaloneCall(t, calls)
	settings.RunMode = RunModeDevice
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("mode switch did not cancel standalone")
	}
	if dials.Load() != 0 {
		t.Fatal("device started before standalone joined")
	}
	if s.Snapshot().Voice.Connected {
		t.Fatal("canceled voice remained usable during mode switch")
	}
	if err := s.VoiceCommand("wake", ""); !errors.Is(err, ErrOffline) {
		t.Fatalf("command accepted during mode switch: %v", err)
	}
	old.emit(voice.State{Connected: true, Response: "late"})
	if s.Snapshot().Voice.Response != "" {
		t.Fatal("canceled mode accepted callback")
	}
	close(release)
	select {
	case <-device:
	case <-time.After(time.Second):
		t.Fatal("device mode did not start promptly")
	}
	old.emit(voice.State{Connected: true, Response: "late after switch"})
	if !s.Snapshot().Connected || s.Snapshot().Voice.Response != "" {
		t.Fatal("old standalone callback replaced device state")
	}
	settings.RunMode, settings.Voice.Enabled = RunModeStandalone, false
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		state := s.Snapshot()
		return state.Status == "standalone" && !state.Connected && state.Voice.Phase == "disabled"
	})
}

func TestStandaloneReloadInterruptsVoiceRetryAndRejectsEndedCallbacks(t *testing.T) {
	deps := testDeps()
	calls := make(chan standaloneCall, 4)
	fail := make(chan struct{})
	var runs atomic.Int32
	deps.voiceRunStandalone = func(ctx context.Context, cfg voice.Config, _ voice.Host, commands <-chan voice.Command, emit func(voice.State)) error {
		n := runs.Add(1)
		emit(voice.State{Connected: true, Phase: "idle"})
		calls <- standaloneCall{cfg, emit, commands}
		if n == 1 {
			<-fail
			return errors.New("capture ended")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	s := testService(t, deps)
	settings := s.Snapshot().Settings
	settings.RunMode, settings.Voice.Enabled = RunModeStandalone, true
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	old := receiveStandaloneCall(t, calls)
	generation := s.voiceGeneration.Load()
	close(fail)
	eventually(t, func() bool { return s.voiceGeneration.Load() != generation })
	old.emit(voice.State{Connected: true, Response: "ended session"})
	if state := s.Snapshot(); state.Voice.Connected || state.Voice.Response != "" {
		t.Fatal("ended worker callback accepted during retry")
	}
	if err := s.ReloadWakeModels(settings.Voice.WakeModelDir); err != nil {
		t.Fatal(err)
	}
	receiveStandaloneCall(t, calls)
}

func TestStoppedStandaloneSettingsDoNotStartVoice(t *testing.T) {
	deps := testDeps()
	var runs atomic.Int32
	deps.voiceRunStandalone = func(context.Context, voice.Config, voice.Host, <-chan voice.Command, func(voice.State)) error {
		runs.Add(1)
		return nil
	}
	s := testService(t, deps)
	settings := s.Snapshot().Settings
	settings.RunMode, settings.Voice.Enabled = RunModeStandalone, true
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadWakeModels(settings.Voice.WakeModelDir); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Running || runs.Load() != 0 {
		t.Fatal("saving stopped settings started capture")
	}
	settings.Voice.Enabled = false
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Snapshot().Voice.Phase == "disabled" })
	if runs.Load() != 0 || s.Snapshot().Connected {
		t.Fatal("disabled standalone opened voice or device")
	}
}

func TestModeSwitchInterruptsDeviceReconnectBackoff(t *testing.T) {
	deps := testDeps()
	deps.retryEvery = time.Hour
	attempted := make(chan struct{}, 1)
	deps.find = func(context.Context) (string, error) { attempted <- struct{}{}; return "", errors.New("offline") }
	calls := make(chan standaloneCall, 1)
	deps.voiceRunStandalone = func(ctx context.Context, cfg voice.Config, _ voice.Host, commands <-chan voice.Command, emit func(voice.State)) error {
		calls <- standaloneCall{cfg, emit, commands}
		<-ctx.Done()
		return ctx.Err()
	}
	s := testService(t, deps)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("no discovery attempt")
	}
	eventually(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.attemptCancel == nil })
	settings := s.Snapshot().Settings
	settings.RunMode, settings.Voice.Enabled = RunModeStandalone, true
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	receiveStandaloneCall(t, calls)
}
