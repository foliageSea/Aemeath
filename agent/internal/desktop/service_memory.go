package desktop

import "aemeath/agent/internal/brain"

// ListMemories is independent of device and voice connectivity. Memory bodies are
// fetched only by the manager, never copied into the global State.
func (s *Service) ListMemories() brain.MemorySnapshot {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return brain.MemorySnapshot{Error: ErrClosed.Error()}
	}
	return s.longTermMemory.Snapshot()
}

func (s *Service) SaveMemory(entry brain.MemoryEntry, expected uint64) (brain.MemoryEntry, error) {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return brain.MemoryEntry{}, ErrClosed
	}
	entry.Source = "manual"
	return s.longTermMemory.Save(entry, expected)
}

func (s *Service) DeleteMemory(id string, expected uint64) error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return s.longTermMemory.Delete(id, expected)
}

func (s *Service) ClearMemories(expected uint64) error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return s.longTermMemory.Clear(expected)
}

func (h voiceHost) LongTermMemory() *brain.LongTermMemory { return h.service.longTermMemory }
