package desktop

import (
	"reflect"

	"aemeath/agent/internal/voice"
)

// VoiceUpdates is a single-consumer, coalesced invalidation stream for the UI.
// Read Snapshot after a notification; notifications do not carry stale snapshots.
// Slow consumers never block the voice worker. Close closes this stream.
func (s *Service) VoiceUpdates() <-chan struct{} { return s.voiceUpdates }

// setVoiceLocked publishes state under s.mu. Timing-only PCM progress stays on
// the regular low-frequency refresh; text, tools and phase changes notify promptly.
func (s *Service) setVoiceLocked(next voice.State) {
	previous, comparable := s.state.Voice, next
	previous.ASRMillis, previous.AgentMillis, previous.TTSMillis = 0, 0, 0
	comparable.ASRMillis, comparable.AgentMillis, comparable.TTSMillis = 0, 0, 0
	changed := !reflect.DeepEqual(previous, comparable)
	s.state.Voice = next.Clone()
	if changed && !s.closed {
		signal(s.voiceUpdates)
	}
}
