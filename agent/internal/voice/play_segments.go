package voice

import (
	"errors"
	"time"
)

type speechClip struct {
	pcm  []int16
	text string
}

func (s *session) advanceWireTurn() error {
	if s.wireTurn == ^uint32(0) {
		return errors.New("voice playback identifier exhausted; reconnect required")
	}
	s.wireTurn++
	return nil
}

// Every clip uses a fresh v1 wire turn. The logical UI turn stays unchanged;
// old credit/playing/played events cannot reset or drain a later clip.
func (s *session) nextSegment(drained bool) error {
	if s.play != nil {
		return nil
	}
	if len(s.queuedAudio) == 0 && s.generationDone {
		if err := s.abort(false); err != nil {
			return err
		}
		s.state.Error = s.diagnostic
		return s.phase("idle")
	}
	if drained {
		if err := s.advanceWireTurn(); err != nil {
			return err
		}
	}
	if len(s.queuedAudio) == 0 {
		return s.phase("synthesizing")
	}
	clip := s.queuedAudio[0]
	pcm := clip.pcm
	s.queuedAudio[0] = speechClip{}
	s.queuedAudio = s.queuedAudio[1:]
	if len(pcm) == 0 || len(pcm) > maxPlaybackSamples {
		return s.fail("playback", errors.New("invalid audio segment length"))
	}
	s.play = &playback{pcm: pcm}
	duration := time.Duration(len(pcm)) * time.Second / sampleRate
	s.deadline = time.Now().Add(duration + 15*time.Second)
	if err := s.startPlayback(false, len(pcm)); err != nil {
		return err
	}
	return s.send(message{Type: "response", Turn: s.state.Turn, Text: clip.text})
}
