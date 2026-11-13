package brain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	longTermMemoryVersion  = 1
	maxMemoryEntries       = 200
	maxMemoryContentRunes  = 256
	maxMemoryKeyRunes      = 80
	maxMemoryEvidenceRunes = 512
	maxMemoryFileBytes     = 1 << 20
)

var (
	ErrMemoryConflict    = errors.New("long-term memory revision conflict")
	ErrMemoryUnavailable = errors.New("long-term memory unavailable")
	ErrMemoryInvalid     = errors.New("invalid long-term memory entry")
	ErrMemoryLimit       = errors.New("long-term memory limit exceeded")
	ErrMemoryNotFound    = errors.New("long-term memory entry not found")
	ErrMemoryProtected   = errors.New("long-term memory source is protected")
)

// MemoryEntry is independent of chat history. Evidence is retained verbatim.
type MemoryEntry struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Category  string    `json:"category"`
	Content   string    `json:"content"`
	Source    string    `json:"source"`
	Evidence  string    `json:"evidence"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type MemorySnapshot struct {
	Entries  []MemoryEntry `json:"entries"`
	Revision uint64        `json:"revision"`
	Epoch    uint64        `json:"epoch"`
	Error    string        `json:"error"`
	Status   string        `json:"status"`
}

type longTermMemoryFile struct {
	Version  int           `json:"version"`
	Revision uint64        `json:"revision"`
	Entries  []MemoryEntry `json:"entries"`
}

// LongTermMemory serializes mutations, including disk commits. A failed commit
// never publishes candidate entries or advances the revision.
type LongTermMemory struct {
	mu          sync.RWMutex
	path        string
	entries     []MemoryEntry
	revision    uint64
	epoch       uint64
	err         string
	unavailable bool
	status      string
}

func NormalizeMemoryMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "":
		return "auto", nil
	case "auto", "explicit", "off":
		return mode, nil
	default:
		return "", fmt.Errorf("%w: unsupported memory mode", ErrMemoryInvalid)
	}
}

// OpenLongTermMemory does not create or rewrite files on load. Only an absent
// file is treated as a new store; malformed or inaccessible files fail closed.
func OpenLongTermMemory(path string) *LongTermMemory {
	m := &LongTermMemory{path: path, entries: []MemoryEntry{}, status: "ready"}
	if path == "" {
		return m
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return m
	}
	if err != nil {
		m.failLoad("cannot open long-term memory")
		return m
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMemoryFileBytes+1))
	if err != nil {
		m.failLoad("cannot read long-term memory")
		return m
	}
	if len(data) > maxMemoryFileBytes {
		m.failLoad("long-term memory file exceeds size limit")
		return m
	}
	if !utf8.Valid(data) {
		m.failLoad("invalid long-term memory encoding")
		return m
	}
	var stored longTermMemoryFile
	if json.Unmarshal(data, &stored) != nil || stored.Version != longTermMemoryVersion {
		m.failLoad("invalid long-term memory file")
		return m
	}
	if len(stored.Entries) > maxMemoryEntries {
		m.failLoad("long-term memory entry limit exceeded")
		return m
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for i, entry := range stored.Entries {
		normalized, err := normalizeMemoryEntry(entry, "")
		if err != nil || normalized.ID == "" || entry.Source == "" || entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() || entry.UpdatedAt.Before(entry.CreatedAt) || ids[normalized.ID] || keys[normalized.Key] {
			m.failLoad("invalid long-term memory entries")
			return m
		}
		ids[normalized.ID], keys[normalized.Key] = true, true
		normalized.CreatedAt = entry.CreatedAt.UTC()
		normalized.UpdatedAt = entry.UpdatedAt.UTC()
		stored.Entries[i] = normalized
	}
	m.entries = append(m.entries, stored.Entries...)
	m.revision = stored.Revision
	return m
}

func (m *LongTermMemory) failLoad(message string) {
	m.err, m.unavailable, m.status = message, true, "unavailable"
}

func (m *LongTermMemory) Snapshot() MemorySnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MemorySnapshot{Entries: cloneMemoryEntries(m.entries), Revision: m.revision, Epoch: m.epoch, Error: m.err, Status: m.status}
}

func cloneMemoryEntries(entries []MemoryEntry) []MemoryEntry {
	return append(make([]MemoryEntry, 0, len(entries)), entries...)
}

func (m *LongTermMemory) checkRevision(expected uint64, allowUnavailable bool) error {
	if m.unavailable && !allowUnavailable {
		return ErrMemoryUnavailable
	}
	if expected != m.revision {
		return ErrMemoryConflict
	}
	if m.revision == ^uint64(0) {
		return ErrMemoryLimit
	}
	return nil
}

func normalizeMemoryEntry(entry MemoryEntry, defaultSource string) (MemoryEntry, error) {
	for _, value := range []string{entry.ID, entry.Key, entry.Category, entry.Content, entry.Source, entry.Evidence} {
		if !utf8.ValidString(value) {
			return MemoryEntry{}, fmt.Errorf("%w: invalid UTF-8", ErrMemoryInvalid)
		}
	}
	entry.ID = strings.TrimSpace(entry.ID)
	entry.Key = strings.ToLower(strings.Join(strings.Fields(entry.Key), " "))
	entry.Content = strings.Join(strings.Fields(entry.Content), " ")
	entry.Category = strings.ToLower(strings.TrimSpace(entry.Category))
	entry.Source = strings.ToLower(strings.TrimSpace(entry.Source))
	if entry.Source == "" {
		entry.Source = defaultSource
	}
	if entry.Key == "" || entry.Content == "" {
		return MemoryEntry{}, fmt.Errorf("%w: key and content are required", ErrMemoryInvalid)
	}
	if utf8.RuneCountInString(entry.ID) > maxMemoryKeyRunes || utf8.RuneCountInString(entry.Key) > maxMemoryKeyRunes || utf8.RuneCountInString(entry.Content) > maxMemoryContentRunes || utf8.RuneCountInString(entry.Evidence) > maxMemoryEvidenceRunes {
		return MemoryEntry{}, ErrMemoryLimit
	}
	switch entry.Category {
	case "profile", "preference", "fact", "goal":
	default:
		return MemoryEntry{}, fmt.Errorf("%w: unsupported category", ErrMemoryInvalid)
	}
	switch entry.Source {
	case "manual", "explicit", "auto":
	default:
		return MemoryEntry{}, fmt.Errorf("%w: unsupported source", ErrMemoryInvalid)
	}
	return entry, nil
}

func memoryEntryIndex(entries []MemoryEntry, entry MemoryEntry) (int, error) {
	byID, byKey := -1, -1
	for i := range entries {
		if entry.ID != "" && entries[i].ID == entry.ID {
			byID = i
		}
		if entries[i].Key == entry.Key {
			byKey = i
		}
	}
	if byID >= 0 {
		if byKey >= 0 && byKey != byID {
			return -1, fmt.Errorf("%w: duplicate key", ErrMemoryInvalid)
		}
		return byID, nil
	}
	return byKey, nil
}

func prepareMemoryEntry(entry MemoryEntry, previous *MemoryEntry) (MemoryEntry, error) {
	now := time.Now().UTC()
	if previous != nil {
		entry.ID, entry.CreatedAt = previous.ID, previous.CreatedAt
		// A restored timestamp or a clock correction must not make the next
		// committed document fail its own timestamp validation on restart.
		if now.Before(previous.UpdatedAt) {
			now = previous.UpdatedAt.UTC()
		}
	} else {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return MemoryEntry{}, fmt.Errorf("%w: cannot generate ID", ErrMemoryUnavailable)
		}
		entry.ID, entry.CreatedAt = hex.EncodeToString(id[:]), now
	}
	entry.UpdatedAt = now
	return entry, nil
}

func (m *LongTermMemory) Save(entry MemoryEntry, expected uint64) (MemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(expected, false); err != nil {
		return MemoryEntry{}, err
	}
	entry, err := normalizeMemoryEntry(entry, "manual")
	if err != nil {
		return MemoryEntry{}, err
	}
	index, err := memoryEntryIndex(m.entries, entry)
	if err != nil {
		return MemoryEntry{}, err
	}
	var previous *MemoryEntry
	if index >= 0 {
		previous = &m.entries[index]
		if entry.Source == "auto" && previous.Source != "auto" {
			return MemoryEntry{}, ErrMemoryProtected
		}
	} else if len(m.entries) >= maxMemoryEntries {
		return MemoryEntry{}, ErrMemoryLimit
	}
	entry, err = prepareMemoryEntry(entry, previous)
	if err != nil {
		return MemoryEntry{}, err
	}
	next := cloneMemoryEntries(m.entries)
	if index >= 0 {
		next[index] = entry
	} else {
		next = append(next, entry)
	}
	if err := m.commit(next); err != nil {
		return MemoryEntry{}, err
	}
	m.epoch++
	m.resetWorkStatus()
	return entry, nil
}

func (m *LongTermMemory) Delete(id string, expected uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(expected, false); err != nil {
		return err
	}
	for i, entry := range m.entries {
		if entry.ID == id {
			next := cloneMemoryEntries(m.entries)
			next = append(next[:i], next[i+1:]...)
			if err := m.commit(next); err != nil {
				return err
			}
			m.epoch++
			m.resetWorkStatus()
			return nil
		}
	}
	return ErrMemoryNotFound
}

// Clear commits an empty document, including when recovering a corrupt file.
// Removing the file would allow old contents to reappear after a failed delete.
func (m *LongTermMemory) Clear(expected uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(expected, true); err != nil {
		return err
	}
	if err := m.commit([]MemoryEntry{}); err != nil {
		return err
	}
	m.epoch++
	m.unavailable, m.status = false, "ready"
	return nil
}

// ApplyAuto validates every candidate before changing anything. Protected
// entries are skipped, even when addressed by ID. Evidence authenticity is
// checked by the engine against the original conversation before this call.
func (m *LongTermMemory) ApplyAuto(entries []MemoryEntry, expected uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(expected, false); err != nil {
		return err
	}
	if len(entries) > maxMemoryEntries {
		return ErrMemoryLimit
	}
	normalized := make([]MemoryEntry, len(entries))
	for i, entry := range entries {
		var err error
		normalized[i], err = normalizeMemoryEntry(entry, "auto")
		if err != nil {
			return err
		}
		if normalized[i].Source != "auto" {
			return fmt.Errorf("%w: automatic entries require auto source", ErrMemoryInvalid)
		}
	}
	next := cloneMemoryEntries(m.entries)
	changed := false
	for _, entry := range normalized {
		index, err := memoryEntryIndex(next, entry)
		if err != nil {
			return err
		}
		var previous *MemoryEntry
		if index >= 0 {
			previous = &next[index]
			if previous.Source != "auto" {
				continue
			}
		} else if len(next) >= maxMemoryEntries {
			return ErrMemoryLimit
		}
		entry, err = prepareMemoryEntry(entry, previous)
		if err != nil {
			return err
		}
		if index >= 0 {
			next[index] = entry
		} else {
			next = append(next, entry)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return m.commit(next)
}

// Invalidate only advances the in-process revision; the next successful commit
// persists it. Pending work status returns to ready unless loading failed.
func (m *LongTermMemory) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revision != ^uint64(0) {
		m.revision++
	}
	m.epoch++
	m.resetWorkStatus()
}

// resetWorkStatus requires the write lock and preserves non-work statuses.
func (m *LongTermMemory) resetWorkStatus() {
	if !m.unavailable && (m.status == "queued" || m.status == "extracting") {
		m.status = "ready"
	}
}

func (m *LongTermMemory) SetStatus(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.unavailable {
		m.status = status
	}
}

func (m *LongTermMemory) commit(entries []MemoryEntry) error {
	nextRevision := m.revision + 1
	if m.path != "" {
		data, err := json.Marshal(longTermMemoryFile{Version: longTermMemoryVersion, Revision: nextRevision, Entries: entries})
		if err != nil {
			return fmt.Errorf("%w: cannot encode memory", ErrMemoryInvalid)
		}
		if len(data) > maxMemoryFileBytes {
			return ErrMemoryLimit
		}
		if err := writeLongTermMemory(m.path, data); err != nil {
			m.err = "cannot persist long-term memory"
			return fmt.Errorf("%w: cannot persist memory: %v", ErrMemoryUnavailable, err)
		}
	}
	m.entries, m.revision, m.err = entries, nextRevision, ""
	return nil
}

func writeLongTermMemory(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".long-term-memory-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func (m *LongTermMemory) Search(query string, limit int) []MemoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.unavailable {
		return []MemoryEntry{}
	}
	return SearchMemories(m.entries, query, limit)
}

// memorySearchTokens combines contiguous Han bigrams with case-insensitive
// words. A one-character Han query remains useful on its own.
func memorySearchTokens(text string) map[string]bool {
	tokens := map[string]bool{}
	run := []rune{}
	han := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if han && len(run) > 1 {
			for i := 1; i < len(run); i++ {
				tokens[string(run[i-1:i+1])] = true
			}
		} else {
			tokens[string(run)] = true
		}
		run = run[:0]
	}
	for _, r := range strings.ToLower(text) {
		isHan := unicode.Is(unicode.Han, r)
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			flush()
			continue
		}
		if len(run) > 0 && isHan != han {
			flush()
		}
		han = isHan
		run = append(run, r)
	}
	flush()
	return tokens
}

// SearchMemories returns independent results, sorted stably by relevance,
// explicit/manual/auto source and recency. At most two unmatched profile or
// preference entries supplement a nonempty query; an empty query lists all.
func SearchMemories(entries []MemoryEntry, query string, limit int) []MemoryEntry {
	if limit <= 0 {
		return []MemoryEntry{}
	}
	if limit > maxMemoryEntries {
		limit = maxMemoryEntries
	}
	query = strings.ToLower(strings.Join(strings.Fields(query), " "))
	terms := memorySearchTokens(query)
	type rankedEntry struct {
		entry MemoryEntry
		score int
	}
	ranked := make([]rankedEntry, 0, len(entries))
	for _, entry := range entries {
		score := 0
		keyTerms, contentTerms := memorySearchTokens(entry.Key), memorySearchTokens(entry.Content)
		for term := range terms {
			if keyTerms[term] {
				score += 3
			} else if contentTerms[term] {
				score++
			} else if utf8.RuneCountInString(term) == 1 && unicode.Is(unicode.Han, []rune(term)[0]) && strings.Contains(entry.Key+" "+entry.Content, term) {
				score++
			}
		}
		if score > 0 && query != "" {
			if strings.Contains(strings.ToLower(entry.Key), query) {
				score += 4
			}
			if strings.Contains(strings.ToLower(entry.Content), query) {
				score += 2
			}
		}
		ranked = append(ranked, rankedEntry{entry, score})
	}
	sourceRank := func(source string) int {
		switch source {
		case "explicit":
			return 3
		case "manual":
			return 2
		case "auto":
			return 1
		}
		return 0
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if sourceRank(a.entry.Source) != sourceRank(b.entry.Source) {
			return sourceRank(a.entry.Source) > sourceRank(b.entry.Source)
		}
		return a.entry.UpdatedAt.After(b.entry.UpdatedAt)
	})
	results := make([]MemoryEntry, 0, min(limit, len(entries)))
	fallback := 0
	for _, item := range ranked {
		if len(results) == limit {
			break
		}
		if query != "" && item.score == 0 {
			if fallback >= 2 || (item.entry.Category != "profile" && item.entry.Category != "preference") {
				continue
			}
			fallback++
		}
		results = append(results, item.entry)
	}
	return results
}
