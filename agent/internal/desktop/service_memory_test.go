package desktop

import (
	"errors"
	"path/filepath"
	"testing"

	"aemeath/agent/internal/brain"
)

func TestLongTermMemoryOfflineManagementAndConversationSeparation(t *testing.T) {
	s := testService(t, testDeps())
	if s.Snapshot().Connected {
		t.Fatal("fixture must be offline")
	}
	initial := s.ListMemories()
	entry, err := s.SaveMemory(brain.MemoryEntry{Key: "drink", Category: "preference", Content: "喜欢绿茶", Source: "auto"}, initial.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Source != "manual" {
		t.Fatalf("desktop editing must force manual source: %+v", entry)
	}
	restored := brain.OpenLongTermMemory(filepath.Join(filepath.Dir(s.path), "agent_memory.json")).Snapshot()
	if len(restored.Entries) != 1 || restored.Entries[0].ID != entry.ID {
		t.Fatalf("memory not persisted alongside settings: %+v", restored)
	}

	// Clearing conversation memory must not affect independently managed facts.
	s.memory.Clear()
	snapshot := s.ListMemories()
	if len(snapshot.Entries) != 1 {
		t.Fatal("conversation clear deleted long term memory")
	}
	snapshot.Entries[0].Content = "caller mutation"
	if s.ListMemories().Entries[0].Content != "喜欢绿茶" {
		t.Fatal("list leaked store ownership")
	}
	entry.Content = "喜欢乌龙茶"
	if _, err := s.SaveMemory(entry, initial.Revision); err == nil {
		t.Fatal("stale edit overwrote memory")
	}
	entry, err = s.SaveMemory(entry, s.ListMemories().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMemory(entry.ID, initial.Revision); err == nil {
		t.Fatal("stale deletion accepted")
	}
	if err := s.DeleteMemory(entry.ID, s.ListMemories().Revision); err != nil {
		t.Fatal(err)
	}
	if len(s.ListMemories().Entries) != 0 {
		t.Fatal("delete did not remove entry")
	}
	entry.ID = ""
	if _, err := s.SaveMemory(entry, s.ListMemories().Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMemories(initial.Revision); err == nil {
		t.Fatal("stale clear accepted")
	}
	if err := s.ClearMemories(s.ListMemories().Revision); err != nil {
		t.Fatal(err)
	}
	if len(s.ListMemories().Entries) != 0 {
		t.Fatal("clear did not remove entries")
	}

	s.Close()
	if s.ListMemories().Error != ErrClosed.Error() {
		t.Fatal("closed list must report error")
	}
	if _, err := s.SaveMemory(entry, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed save: %v", err)
	}
	if err := s.DeleteMemory(entry.ID, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed delete: %v", err)
	}
	if err := s.ClearMemories(0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed clear: %v", err)
	}
}

func TestMemoryModeSaveInvalidatesBackgroundRevisionAndKeepsEntries(t *testing.T) {
	s := testService(t, testDeps())
	before := s.ListMemories()
	_, err := s.SaveMemory(brain.MemoryEntry{Key: "name", Category: "profile", Content: "小明"}, before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before = s.ListMemories()
	canceled := false
	s.mu.Lock()
	s.attemptCancel = func() { canceled = true }
	s.mu.Unlock()
	settings := s.Snapshot().Settings
	settings.Voice.MemoryMode = "off"
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	after := s.ListMemories()
	if after.Revision == before.Revision {
		t.Fatal("mode change did not invalidate pending extraction revision")
	}
	if !canceled || s.Snapshot().Settings.Voice.MemoryMode != "off" {
		t.Fatal("mode change did not use existing voice restart mechanism")
	}
	if len(after.Entries) != 1 {
		t.Fatal("off deleted stored data")
	}
	if (voiceHost{s}).LongTermMemory() != s.longTermMemory {
		t.Fatal("voice does not share the desktop store")
	}
	settings.Voice.MemoryMode = "explicit"
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if len(s.ListMemories().Entries) != 1 {
		t.Fatal("mode transition lost entries")
	}
}

func TestEmptySettingsPathUsesIndependentMemoryStores(t *testing.T) {
	settings, err := normalizeSettings(Settings{})
	if err != nil {
		t.Fatal(err)
	}
	first := newService("", settings, testDeps())
	defer first.Close()
	_, err = first.SaveMemory(brain.MemoryEntry{Key: "name", Category: "profile", Content: "小明"}, first.ListMemories().Revision)
	if err != nil {
		t.Fatal(err)
	}
	second := newService("", settings, testDeps())
	defer second.Close()
	if len(second.ListMemories().Entries) != 0 {
		t.Fatal("empty path must not persist or share memory")
	}
}
