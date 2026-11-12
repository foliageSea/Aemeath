package desktop

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
)

func TestVoiceUpdatesCoalesceAndIgnoreTimingProgress(t *testing.T) {
	s := testService(t, testDeps())
	publish := func(state voice.State) { s.mu.Lock(); defer s.mu.Unlock(); s.setVoiceLocked(state) }
	state := voice.State{Phase: "thinking", Transcript: "hello", Turn: 1}
	for i := 0; i < 1000; i++ {
		state.Response = fmt.Sprint(i)
		publish(state)
	}
	if len(s.voiceUpdates) != 1 {
		t.Fatalf("unbounded notifications: %d", len(s.voiceUpdates))
	}
	<-s.VoiceUpdates()
	if got := s.Snapshot().Voice.Response; got != "999" {
		t.Fatalf("stale response %q", got)
	}
	for i := int64(1); i < 1000; i++ {
		state.TTSMillis = i
		state.AgentMillis = i
		state.ASRMillis = i
		publish(state)
	}
	select {
	case <-s.VoiceUpdates():
		t.Fatal("timing-only PCM progress notified UI")
	default:
	}
	if s.Snapshot().Voice.TTSMillis != 999 {
		t.Fatal("timing progress was lost")
	}
	changes := []func(){
		func() { state.Phase = "speaking" },
		func() { state.FirstAudioMillis = 20 },
		func() { state.ToolCalls = []brain.ToolCall{{Name: "web_search", Status: "running"}} },
		func() { state.ToolCalls[0].Status = "succeeded" },
		func() { state.Error = "failed" },
	}
	for i, change := range changes {
		change()
		publish(state)
		select {
		case <-s.VoiceUpdates():
		default:
			t.Fatalf("semantic change %d did not notify", i)
		}
	}
	s.Close()
	for range s.VoiceUpdates() {
	}
	publish(voice.State{Phase: "late"}) // must not send to a closed stream
	s.Close()
}

func TestVoiceUpdatesFromSessionIgnoreCanceledEmits(t *testing.T) {
	states := make(chan voice.State)
	applied := make(chan struct{})
	deps := testDeps()
	deps.voiceRun = func(ctx context.Context, _ string, _ voice.Config, _ voice.Host, _ <-chan voice.Command, emit func(voice.State)) error {
		for {
			select {
			case state := <-states:
				emit(state)
				applied <- struct{}{}
			case <-ctx.Done():
				emit(voice.State{Response: "stale"})
				return ctx.Err()
			}
		}
	}
	s := testService(t, deps)
	conn := &fakeConn{}
	s.mu.Lock()
	s.conn = conn
	s.state.Settings.Voice.Enabled = true
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := s.startVoice(ctx, conn, "device")
	<-s.VoiceUpdates() // connecting
	states <- voice.State{Connected: true, Phase: "thinking", Response: "live"}
	<-applied
	select {
	case <-s.VoiceUpdates():
	case <-time.After(time.Second):
		t.Fatal("session response did not notify")
	}
	if s.Snapshot().Voice.Response != "live" {
		t.Fatal("response missing")
	}
	cancel()
	<-done
	if s.Snapshot().Voice.Response != "live" {
		t.Fatal("canceled session overwrote response")
	}
	select {
	case <-s.VoiceUpdates():
	case <-time.After(time.Second):
		t.Fatal("disconnect did not notify")
	}
}
