package voice

import (
	"errors"
	"time"

	"aemeath/agent/internal/audiooutput"
)

// consoleOutput is nonblocking: a short Write means its bounded queue is full.
// Status reports rendering, not merely acceptance of PCM into the queue.
// Each instance belongs to one clip/stream; Close discards outstanding audio.
type consoleOutput interface {
	Write([]int16) (int, error)
	Finish()
	Status() (started, drained bool, err error)
	Close()
}

func newConsoleOutput(c Config) (consoleOutput, error) {
	return audiooutput.New(sampleRate, c.Volume)
}

func (s *session) closeOutput() {
	if s.output != nil {
		s.output.Close()
		s.output = nil
		if s.standalone {
			s.resetCapture()
		}
	}
}

func (s *session) startPlayback(streaming bool, samples int) error {
	if s.cfg.PlaybackOutput != "console" {
		return s.send(message{Type: "playback_start", Turn: s.state.Turn, Streaming: streaming, Samples: samples, Volume: s.cfg.Volume})
	}
	if s.standalone {
		s.resetCapture()
	}
	if s.newOutput == nil {
		return s.fail("playback", errors.New("console audio output is unavailable"))
	}
	out, err := s.newOutput(s.cfg)
	if err != nil {
		return s.fail("playback", err)
	}
	if out == nil {
		return s.fail("playback", errors.New("console audio output is unavailable"))
	}
	s.output = out
	return s.pumpConsole()
}

func (s *session) pumpConsole() error {
	p := s.play
	if p == nil || s.output == nil {
		return nil
	}
	if !p.ended {
		if p.offset < len(p.pcm) {
			n, err := s.output.Write(p.pcm[p.offset:])
			if err != nil {
				return s.fail("playback", err)
			}
			if n < 0 || n > len(p.pcm)-p.offset {
				return s.fail("playback", errors.New("invalid console audio write count"))
			}
			p.offset += n
		}
		if p.offset == len(p.pcm) && (!p.streaming || p.inputEnded) {
			p.ended = true
			s.output.Finish()
		}
	}
	started, drained, err := s.output.Status()
	if err != nil {
		return s.fail("playback", err)
	}
	if started && !p.playing {
		p.playing = true
		if s.state.FirstAudioMillis == 0 {
			s.state.FirstAudioMillis = max(1, time.Since(s.cloudStarted).Milliseconds())
		}
		if err := s.phase("speaking"); err != nil {
			return err
		}
	}
	if drained {
		if !p.ended || !p.playing {
			return s.fail("playback", errors.New("console playback drained before rendered audio"))
		}
		s.state.PlayedSegments++
		s.closeOutput()
		s.play = nil
		return s.nextSegment(true)
	}
	return nil
}
