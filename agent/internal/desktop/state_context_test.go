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

func TestSnapshotEstimatesRestoredContextBeforeVoiceStarts(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			data, err := json.Marshal(map[string]any{
				"version": 2, "updated_at": time.Now(), "summary": "用户喜欢清淡口味",
				"turns":        []map[string]string{{"user": "推荐晚饭", "assistant": "可以喝粥"}},
				"compressions": 3, "dropped_turns": 2, "last_compression": "compressed",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), "voice_history.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			settings, err := normalizeSettings(Settings{Voice: voice.Config{LLMModel: "model", ContextWindowMode: mode, ContextWindow: 65536, LLMTools: true}})
			if err != nil {
				t.Fatal(err)
			}
			service := newService(path, settings, testDeps())
			defer service.Close()
			state := service.Snapshot()
			stats := state.Voice.Context
			if state.Running || stats.EstimatedTokens <= 0 || stats.InputBudget <= 0 || stats.UsagePercent <= 0 || stats.ContextWindow != 65536 || stats.RetainedTurns != 1 || stats.SummaryTokens <= 0 || stats.Compressions != 3 || stats.DroppedTurns != 2 || stats.LastCompression != "compressed" || stats.UsageAvailable {
				t.Fatalf("startup still needs a voice request to restore context: %+v", stats)
			}
			source := "manual"
			if mode == "auto" {
				source = "detecting"
			}
			if stats.ContextWindowSource != source {
				t.Fatalf("startup claimed unverified model capacity: %+v", stats)
			}
			settings.Voice.ContextWindow = 32768
			if err := service.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			if got := service.Snapshot().Voice.Context; got.ContextWindow != 32768 || got.InputBudget == stats.InputBudget || got.EstimatedTokens != stats.EstimatedTokens {
				t.Fatalf("saved settings did not refresh startup estimate: %+v", got)
			}
			service.memory.Clear()
			if got := service.Snapshot().Voice.Context; got.InputBudget <= 0 || got.RetainedTurns != 0 || got.SummaryTokens != 0 || got.Compressions != 0 || got.DroppedTurns != 0 {
				t.Fatalf("cleared startup estimate retained history: %+v", got)
			}
		})
	}
}
