package voice

import (
	"errors"
	"time"
)

const streamBufferSamples = sampleRate * 2

func (s *session) streamQueueFull() bool {
	return s.play != nil && s.play.streaming && len(s.play.pcm)-s.play.offset > streamBufferSamples-frameSamples
}
func (s *session) startAudioStream() error {
	if s.play != nil || len(s.queuedAudio) != 0 {
		return s.fail("stream_start", errors.New("playback is already active"))
	}
	s.play = &playback{streaming: true}
	s.state.GeneratedSegments = 1
	s.deadline = time.Now().Add(synthesisBudget + 15*time.Second)
	s.emitState()
	return s.startPlayback(true, 0)
}
func (s *session) appendAudioStream(pcm []int16) error {
	p := s.play
	if p == nil || !p.streaming || p.inputEnded || len(pcm) == 0 || len(pcm) > frameSamples || len(pcm) > maxPlaybackSamples-p.received {
		return s.fail("stream_audio", errors.New("invalid streaming PCM"))
	}
	unread := len(p.pcm) - p.offset
	if unread+len(pcm) > streamBufferSamples {
		return s.fail("stream_audio", errors.New("streaming PCM buffer exceeded"))
	}
	if p.offset > 0 {
		copy(p.pcm, p.pcm[p.offset:])
		p.pcm = p.pcm[:unread]
		p.offset = 0
	}
	s.captureReplay(pcm)
	p.pcm = append(p.pcm, pcm...)
	p.received += len(pcm)
	return s.pump()
}
func (s *session) finishAudioStream(samples int) error {
	p := s.play
	if p == nil || !p.streaming || p.inputEnded || samples <= 0 || samples != p.received {
		return s.fail("stream_end", errors.New("stream sample count mismatch"))
	}
	s.commitReplay()
	p.inputEnded = true
	s.generationDone = true
	// The backend has finished, but queued PCM still needs actual DMA drain.
	s.deadline = time.Now().Add(time.Duration(len(p.pcm)-p.offset)*time.Second/sampleRate + 20*time.Second)
	return s.pump()
}
