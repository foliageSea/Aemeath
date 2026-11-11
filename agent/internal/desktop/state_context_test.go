package desktop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
)

func TestContextModeSaveReconnectAndReload(t *testing.T) {
	deps := testDeps()
	calls := make(chan voice.Config, 4)
	deps.voiceRun = func(ctx context.Context, _ string, cfg voice.Config, _ voice.Host, _ <-chan voice.Command, emit func(voice.State)) error {
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
	checkSession := func(mode string, capacity int) {
		t.Helper()
		select {
		case got := <-calls:
			if got.ContextWindowMode != mode || got.ContextWindow != capacity {
				t.Fatalf("voice session lost context mode/value: %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("saving mode did not reconnect voice")
		}
		stored, err := loadSettings(s.path)
		if err != nil || stored.Voice.ContextWindowMode != mode || stored.Voice.ContextWindow != capacity {
			t.Fatalf("context mode/value did not survive reload: %+v, %v", stored.Voice, err)
		}
	}
	checkSession("auto", 32768)
	for _, mode := range []string{"manual", "auto"} {
		settings := s.Snapshot().Settings
		settings.Voice.ContextWindowMode = mode
		settings.Voice.ContextWindow = 65536
		if err := s.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		checkSession(mode, 65536)
	}
	settings := s.Snapshot().Settings
	settings.Voice.ContextWindowMode = "invalid"
	if err := s.SaveSettings(settings); err == nil {
		t.Fatal("invalid context mode was saved")
	}
	stored, err := loadSettings(s.path)
	if err != nil || stored.Voice.ContextWindowMode != "auto" {
		t.Fatal("invalid save changed persisted context mode")
	}
}

func TestSnapshotReadsContextDirectlyFromMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice_history.json")
	data, err := json.Marshal(map[string]any{
		"version": 1, "updated_at": time.Now(),
		"turns": []map[string]string{{"user": "旧问题", "assistant": "旧回答"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	memory := brain.OpenMemory(path)
	service := &Service{memory: memory, state: State{Voice: voice.State{Context: brain.ContextStats{RetainedTurns: 99}}}}
	first := service.Snapshot()
	if first.Voice.Context != memory.ContextStats() || first.Voice.Context.RetainedTurns != 1 {
		t.Fatalf("snapshot returned stale voice-event context: %+v", first.Voice.Context)
	}
	// Mutate shared memory without emitting a voice event.
	memory.Clear()
	next := service.Snapshot()
	if next.Voice.Context != memory.ContextStats() || next.Voice.Context.RetainedTurns != 0 {
		t.Fatalf("snapshot did not observe memory change: %+v", next.Voice.Context)
	}
	if first.Voice.Context.RetainedTurns != 1 {
		t.Fatal("new memory state mutated a retained value snapshot")
	}
}
