package voice

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Wake interaction follows smart-speaker conventions: a short spoken "我在"
// confirms the wake, and the question is captured only after it has finished.
// The clip is synthesized once with the configured Fish voice
// (agent/scripts/wake-cue-gen), trimmed and normalized to -3 dBFS peak as
// 16 kHz mono PCM16, and embedded so waking never calls cloud TTS.
//
//go:embed assets/wake_cue.wav
var wakeCueWAV []byte

// Echo of the cue (DMA/driver latency, room tail) must not reach VAD.
const wakeCueTailSamples = sampleRate * 15 / 100

var wakeCuePCM = mustWakeCue(wakeCueWAV)

func mustWakeCue(b []byte) []int16 {
	pcm, err := decodeWakeCue(b)
	if err != nil {
		panic("invalid embedded wake cue: " + err.Error())
	}
	return pcm
}

// decodeWakeCue accepts only RIFF/WAVE PCM16 mono at the session sample rate.
func decodeWakeCue(b []byte) ([]int16, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a WAVE file")
	}
	formatOK := false
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		body := p + 8
		if size < 0 || body+size > len(b) {
			return nil, errors.New("truncated chunk")
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("short fmt chunk")
			}
			f := b[body:]
			format, channels := binary.LittleEndian.Uint16(f), binary.LittleEndian.Uint16(f[2:])
			rate, bits := binary.LittleEndian.Uint32(f[4:]), binary.LittleEndian.Uint16(f[14:])
			if format != 1 || channels != 1 || rate != sampleRate || bits != 16 {
				return nil, fmt.Errorf("unsupported format=%d channels=%d rate=%d bits=%d", format, channels, rate, bits)
			}
			formatOK = true
		case "data":
			if !formatOK {
				return nil, errors.New("data before fmt")
			}
			n := size / 2
			if n == 0 || n > maxPlaybackSamples {
				return nil, errors.New("empty or oversized cue")
			}
			pcm := make([]int16, n)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(b[body+2*i:]))
			}
			return pcm, nil
		}
		p = body + size + size%2
	}
	return nil, errors.New("missing data chunk")
}

// startWakeCue plays the cue before listening. The current wake/listening
// turn waits in the "wake" phase; no microphone audio is captured meanwhile.
func (s *session) startWakeCue(notice string) error {
	s.capture = nil
	s.manualFixed = false
	s.wakeCue = true
	s.wakeCueNotice = notice
	s.cueTail = 0
	s.play = &playback{pcm: wakeCuePCM}
	s.deadline = time.Now().Add(time.Duration(len(wakeCuePCM))*time.Second/sampleRate + 15*time.Second)
	if err := s.phase("wake", notice); err != nil {
		return err
	}
	return s.startPlayback(false, len(wakeCuePCM))
}

// finishWakeCue opens the question capture once the cue has fully rendered.
func (s *session) finishWakeCue() error {
	if !s.wakeCue {
		return errors.New("wake cue is not active")
	}
	s.wakeCue = false
	s.play = nil
	// Late acknowledgements of the cue clip must not affect later playback.
	if err := s.advanceWireTurn(); err != nil {
		return err
	}
	if s.vad != nil {
		s.vad.Reset()
	}
	s.capture = &capture{}
	s.manualFixed = s.vad == nil
	if s.manualFixed {
		s.state.Error = s.diagnostic + "; manual recording uses a fixed 6 second window"
	}
	s.cueTail = wakeCueTailSamples
	s.deadline = time.Now().Add(40 * time.Second)
	return s.phase("listening", s.wakeCueNotice)
}
