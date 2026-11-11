package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
)

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
