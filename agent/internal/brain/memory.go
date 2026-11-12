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

const memoryVersion = 2

type memoryTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// localUsageFile stores cumulative estimates independently of retained context.
type localUsageFile struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type memoryFile struct {
	Version         int             `json:"version"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Turns           []memoryTurn    `json:"turns"`
	Summary         string          `json:"summary,omitempty"`
	Compressions    int             `json:"compressions,omitempty"`
	DroppedTurns    int             `json:"dropped_turns,omitempty"`
	LastCompression string          `json:"last_compression,omitempty"`
	LocalUsage      *localUsageFile `json:"local_usage,omitempty"`
}

// Memory outlives individual Engines (one per voice connection) so that
// inactivity, reconnects and app restarts keep the conversation. An empty path keeps it
// in process memory only. Safe for concurrent use.
type Memory struct {
	mu          sync.Mutex
	path        string
	turns       []memoryTurn
	updated     time.Time
	summary     string
	stats       ContextStats
	statsConfig contextConfigKey
	revision    uint64
	epoch       uint64 // changes only when the conversation is manually cleared
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
	if json.Unmarshal(b, &f) != nil || (f.Version != 1 && f.Version != memoryVersion) {
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
	if usage := f.LocalUsage; usage != nil && usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.InputTokens <= int(^uint(0)>>1)-usage.OutputTokens {
		m.stats.LocalPromptTokens = usage.InputTokens
		m.stats.LocalCompletionTokens = usage.OutputTokens
		m.stats.LocalTotalTokens = usage.InputTokens + usage.OutputTokens
	}
	m.updated = f.UpdatedAt
	if len(f.Summary) <= maxTextBytes {
		m.summary = f.Summary
	}
	m.stats.Compressions, m.stats.DroppedTurns = max(0, f.Compressions), max(0, f.DroppedTurns)
	switch f.LastCompression {
	case "compressed", "fallback", "failed":
		m.stats.LastCompression = f.LastCompression
	}
	return m
}

// MemoryTurn is one remembered exchange, for display.
type MemoryTurn struct{ User, Assistant string }

// Turns returns the conversation the next reply will continue, oldest first.
// Inactivity does not discard retained exchanges or their summary.
func (m *Memory) Turns() []MemoryTurn {
	m.mu.Lock()
	defer m.mu.Unlock()
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

// Clear removes both the in-process conversation and its persisted file.
func (m *Memory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetLocked()
	if m.path != "" {
		if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("brain: cannot delete conversation memory")
		}
	}
}

func (m *Memory) resetLocked() {
	m.turns, m.updated, m.summary = nil, time.Time{}, ""
	m.stats = ContextStats{}
	m.statsConfig = contextConfigKey{}
	m.revision++
	m.epoch++
}

// persist writes atomically with owner-only permissions; transcripts are private.
// Failures keep the in-process copy and never fail the voice turn.
func (m *Memory) persist() {
	if m.path == "" {
		return
	}
	var usage *localUsageFile
	if m.stats.LocalPromptTokens != 0 || m.stats.LocalCompletionTokens != 0 {
		usage = &localUsageFile{InputTokens: m.stats.LocalPromptTokens, OutputTokens: m.stats.LocalCompletionTokens}
	}
	b, err := json.Marshal(memoryFile{
		LocalUsage: usage,
		Version:    memoryVersion, UpdatedAt: m.updated, Turns: m.turns, Summary: m.summary,
		Compressions: m.stats.Compressions, DroppedTurns: m.stats.DroppedTurns, LastCompression: m.stats.LastCompression,
	})
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
