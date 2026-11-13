package desktop

import (
	"errors"
	"strings"
	"testing"

	"aemeath/agent/internal/voice"
)

func TestVoiceCommandRespeakValidatesSavedReplyText(t *testing.T) {
	for _, text := range []string{"", " \n\t", strings.Repeat("a", voice.MaxReplayTextBytes+1), string([]byte{0xff})} {
		s := replayCommandService()
		if err := s.VoiceCommand("respeak", text); err == nil || len(s.voiceCommands) != 0 {
			t.Fatal("accepted invalid saved reply")
		}
	}
	for _, text := range []string{"旧回复", strings.Repeat("a", 4097), strings.Repeat("a", voice.MaxReplayTextBytes)} {
		s := replayCommandService()
		if err := s.VoiceCommand("respeak", text); err != nil {
			t.Fatal(err)
		}
		if got := <-s.voiceCommands; got != (voice.Command{Kind: "respeak", Text: text}) {
			t.Fatal(got)
		}
	}
	s := replayCommandService()
	if err := s.VoiceCommand("respeak", "  旧回复 \n"); err != nil {
		t.Fatal(err)
	}
	if got := <-s.voiceCommands; got.Text != "旧回复" {
		t.Fatal(got)
	}
}

func TestVoiceCommandRespeakRequiresActiveSessionAndQueueSpace(t *testing.T) {
	for _, kind := range []string{"closed", "offline", "disabled", "full"} {
		s := replayCommandService()
		switch kind {
		case "closed":
			s.closed = true
		case "offline":
			s.state.Voice.Connected = false
		case "disabled":
			s.state.Settings.Voice.Enabled = false
		case "full":
			for i := 0; i < cap(s.voiceCommands); i++ {
				s.voiceCommands <- voice.Command{Kind: "cancel"}
			}
		}
		err := s.VoiceCommand("respeak", "历史回复")
		if err == nil {
			t.Fatal("accepted unavailable resynthesis", kind)
		}
		if kind == "closed" && !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
		if kind == "offline" && !errors.Is(err, ErrOffline) {
			t.Fatal(err)
		}
	}
}
