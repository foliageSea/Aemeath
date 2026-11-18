package voice

import (
	"fmt"
	"testing"
)

// Frames dropped after the cue so its echo tail never reaches VAD or ASR.
const cueTailFrames = (wakeCueTailSamples + frameSamples - 1) / frameSamples

// completeWakeCue acts as firmware rendering the wake cue. It returns the wire
// turn used by the cue; the question capture uses the following wire turn.
func (d testDevice) completeWakeCue() (uint32, error) {
	start, err := d.until("playback_start")
	if err != nil {
		return 0, err
	}
	if start.Streaming || start.Samples != len(wakeCuePCM) {
		return 0, fmt.Errorf("unexpected wake cue declaration %+v", start)
	}
	if err = d.send(message{Type: "credit", Turn: start.Turn, Frames: maxCredits}); err != nil {
		return 0, err
	}
	if _, err = d.until("playback_end"); err != nil {
		return 0, err
	}
	if err = d.send(message{Type: "playing", Turn: start.Turn}); err != nil {
		return 0, err
	}
	if err = d.send(message{Type: "played", Turn: start.Turn}); err != nil {
		return 0, err
	}
	return start.Turn, nil
}

// micFrames sends count frames with value starting at seq and returns the next seq.
func (d testDevice) micFrames(seq uint32, value int16, count int) (uint32, error) {
	for range count {
		if err := d.mic(seq, value); err != nil {
			return seq, err
		}
		seq++
	}
	return seq, nil
}

// completeWakeCueDirect drives the cue of a directly constructed device session.
func completeWakeCueDirect(t *testing.T, s *session) {
	t.Helper()
	if !s.wakeCue || s.play == nil || s.state.Phase != "wake" {
		t.Fatalf("wake cue not active: cue=%v phase=%s", s.wakeCue, s.state.Phase)
	}
	turn := s.wireTurn
	recordControl(t, s, message{Type: "credit", Turn: turn, Frames: maxCredits})
	recordControl(t, s, message{Type: "playing", Turn: turn})
	recordControl(t, s, message{Type: "played", Turn: turn})
	if s.wakeCue || s.play != nil || s.capture == nil || s.state.Phase != "wake" || s.cueTail != wakeCueTailSamples || s.wireTurn != turn+1 {
		t.Fatalf("wake cue did not start echo-tail drain: cue=%v phase=%s wire=%d", s.wakeCue, s.state.Phase, s.wireTurn)
	}
}

func TestWakeCueIsShortBoundedAndSmooth(t *testing.T) {
	ms := len(wakeCuePCM) * 1000 / sampleRate
	// The whole cue must fit one credit window and stay short to keep wake snappy.
	if ms < 200 || ms > 800 || len(wakeCuePCM) > maxCredits*frameSamples {
		t.Fatalf("wake cue lasts %dms", ms)
	}
	peak := 0
	for _, v := range wakeCuePCM {
		peak = max(peak, int(v), -int(v))
	}
	if peak < 8000 || peak > 30000 {
		t.Fatalf("wake cue peak=%d", peak)
	}
	if abs16(wakeCuePCM[0]) > 200 || abs16(wakeCuePCM[len(wakeCuePCM)-1]) > 200 {
		t.Fatal("wake cue edges are not faded")
	}
}

func abs16(v int16) int {
	if v < 0 {
		return -int(v)
	}
	return int(v)
}

func TestWakeCueDoesNotFeedKeywordDetector(t *testing.T) {
	s := recordTestSession(t)
	d := &fakeDetector{hitAt: frameSamples, end: frameSamples}
	s.detector = d
	s.state.WakeReady = true
	recordCommand(t, s, "wake")
	turn := s.state.Turn
	recordFrame(t, s, 9)
	completeWakeCueDirect(t, s)
	for range cueTailFrames {
		recordFrame(t, s, 9)
	}
	if d.samples != 0 || s.state.Turn != turn || s.state.Phase != "listening" {
		t.Fatalf("cue audio reached KWS or restarted the turn: samples=%d turn=%d phase=%s", d.samples, s.state.Turn, s.state.Phase)
	}
}

func TestCancelDuringWakeCueStopsWithoutListening(t *testing.T) {
	s := recordTestSession(t)
	s.vad = &fakeVAD{}
	recordCommand(t, s, "wake")
	if !s.wakeCue || s.state.Phase != "wake" || s.capture != nil {
		t.Fatal("manual wake did not start the cue")
	}
	recordCommand(t, s, "cancel")
	if s.wakeCue || s.play != nil || s.capture != nil || s.state.Phase != "idle" {
		t.Fatal("cancel left the wake cue or capture active")
	}
	// A late acknowledgement of the canceled cue must not open listening.
	recordControl(t, s, message{Type: "played", Turn: s.wireTurn})
	if s.capture != nil || s.state.Phase != "idle" {
		t.Fatal("late cue acknowledgement opened listening")
	}
}

func TestDecodeWakeCueRejectsUnsupportedAudio(t *testing.T) {
	if _, err := decodeWakeCue([]byte("RIFF")); err == nil {
		t.Fatal("accepted truncated WAVE")
	}
	stereo := append([]byte(nil), wakeCueWAV...)
	stereo[22] = 2 // channel count in the canonical fmt chunk
	if _, err := decodeWakeCue(stereo); err == nil {
		t.Fatal("accepted stereo cue")
	}
}
