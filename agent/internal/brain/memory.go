package brain

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MemoryTTL ends a conversation after this much inactivity, so a question
// asked the next morning does not silently inherit yesterday's context.
const MemoryTTL = 4 * time.Hour

const memoryVersion = 1

type memoryTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}
type memoryFile struct {
	Version   int          `json:"version"`
	UpdatedAt time.Time    `json:"updated_at"`
	Turns     []memoryTurn `json:"turns"`
}

// Memory outlives individual Engines (one per voice connection) so that
// reconnects and app restarts keep the conversation. An empty path keeps it
// in process memory only. Safe for concurrent use.
type Memory struct {
	mu      sync.Mutex
	path    string
	turns   []memoryTurn
	updated time.Time
}

// OpenMemory loads a persisted conversation. A missing, unreadable or
// malformed file starts an empty conversation instead of failing voice.
func OpenMemory(path string) *Memory {
	m := &Memory{path: path}
	if path == "" {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("brain: cannot read conversation memory; starting empty")
		}
		return m
	}
	var f memoryFile
	if json.Unmarshal(b, &f) != nil || f.Version != memoryVersion {
		log.Printf("brain: ignoring malformed conversation memory")
		return m
	}
	for _, t := range f.Turns {
		if t.User != "" && t.Assistant != "" && len(t.User) <= maxTextBytes && len(t.Assistant) <= maxTextBytes {
			m.turns = append(m.turns, t)
		}
	}
	if len(m.turns) > maxTurns {
		m.turns = m.turns[len(m.turns)-maxTurns:]
	}
	m.updated = f.UpdatedAt
	return m
}

// MemoryTurn is one remembered exchange, for display.
type MemoryTurn struct{ User, Assistant string }

// Turns returns the conversation the next reply will continue, oldest first.
// An expired conversation is reported empty, matching what the model sees.
func (m *Memory) Turns() []MemoryTurn {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.updated.IsZero() && time.Since(m.updated) > MemoryTTL {
		return nil
	}
	out := make([]MemoryTurn, 0, len(m.turns))
	for _, t := range m.turns {
		out = append(out, MemoryTurn{User: t.User, Assistant: t.Assistant})
	}
	return out
}

// snapshot returns complete user/assistant pairs and the last activity time.
func (m *Memory) snapshot() ([]*schema.Message, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*schema.Message, 0, 2*len(m.turns))
	for _, t := range m.turns {
		out = append(out, schema.UserMessage(t.User), schema.AssistantMessage(t.Assistant, nil))
	}
	return out, m.updated
}

// store replaces the conversation with history (user/assistant pairs).
func (m *Memory) store(history []*schema.Message, at time.Time) {
	turns := make([]memoryTurn, 0, len(history)/2)
	for i := 0; i+1 < len(history); i += 2 {
		turns = append(turns, memoryTurn{User: history[i].Content, Assistant: history[i+1].Content})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns, m.updated = turns, at
	m.persist()
}

// Clear removes both the in-process conversation and its persisted file.
func (m *Memory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns, m.updated = nil, time.Time{}
	if m.path != "" {
		if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("brain: cannot delete conversation memory")
		}
	}
}

// persist writes atomically with owner-only permissions; transcripts are private.
// Failures keep the in-process copy and never fail the voice turn.
func (m *Memory) persist() {
	if m.path == "" {
		return
	}
	b, err := json.Marshal(memoryFile{Version: memoryVersion, UpdatedAt: m.updated, Turns: m.turns})
	if err == nil {
		err = os.MkdirAll(filepath.Dir(m.path), 0o700)
	}
	if err == nil {
		var tmp *os.File
		if tmp, err = os.CreateTemp(filepath.Dir(m.path), ".voice-memory-*"); err == nil {
			name := tmp.Name()
			_, err = tmp.Write(b)
			if cerr := tmp.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = os.Chmod(name, 0o600)
			}
			if err == nil {
				err = os.Rename(name, m.path)
			}
			if err != nil {
				_ = os.Remove(name)
			}
		}
	}
	if err != nil {
		log.Printf("brain: cannot save conversation memory")
	}
}
