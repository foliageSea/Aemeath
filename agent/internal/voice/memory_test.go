package voice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
)

type memoryTestHost struct {
	Host
	memory *brain.Memory
}

func (h memoryTestHost) ConversationMemory() *brain.Memory { return h.memory }

func TestClearRestoredMemoryBeforeLazyEngineCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice_history.json")
	b, err := json.Marshal(map[string]any{
		"version": 1, "updated_at": time.Now(),
		"turns": []map[string]string{{"user": "旧问题", "assistant": "旧回答"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	memory := brain.OpenMemory(path)
	if len(memory.Turns()) != 1 {
		t.Fatal("fixture did not load")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	dep := fakeDependencies()
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) {
		t.Error("preview/clear must not create model engine")
		return &fakeBrain{}, nil
	}
	jobs, out := make(chan work, 2), make(chan result, 16)
	go func() {
		defer close(done)
		worker(ctx, Config{MiMoAPIKey: "mimo"}, memoryTestHost{memory: memory}, dep, jobs, out)
	}()
	jobs <- work{ctx: ctx, generation: 1, kind: "clear"}
	// Preview is a queue barrier: finishing it proves clear was processed.
	jobs <- work{ctx: ctx, generation: 1, kind: "preview", text: "你好"}
	for {
		select {
		case r := <-out:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.phase == "audio" || r.phase == "generation_done" {
				if len(memory.Turns()) != 0 {
					t.Fatal("clear kept restored memory")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("clear kept disk memory: %v", err)
				}
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("worker stalled")
		}
	}
}
