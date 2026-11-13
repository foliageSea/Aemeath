package desktop

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"aemeath/agent/internal/voice"
)

// A bare service keeps command validation tests independent of background workers.
func replayCommandService() *Service {
	s := &Service{voiceCommands: make(chan voice.Command, 2)}
	s.state.Settings.Voice.Enabled = true
	s.state.Voice = voice.State{Connected: true, Session: 42, ReplayTurns: []uint32{3, 7}}
	return s
}

func TestVoiceCommandReplayRejectsInvalidTarget(t *testing.T) {
	for _, target := range []string{
		"", "42", ":", "42:", ":7", "42:7:1", "42::7", "42:7\n",
		" 42:7", "42:7 ", "42 :7", "42: 7", "42\n:7", "42:7\x00",
		"+42:7", "-42:7", "42:+7", "42:-7", "0x2a:7", "42:0x7",
		"4_2:7", "42:1_0", "４２:7", "42:７", "4a2:7", "42:1a7",
		"0:7", "42:0", "0:0", "000:7", "42:000",
		"18446744073709551616:7", "42:4294967296", strings.Repeat("9", 4097) + ":7",
	} {
		t.Run(target, func(t *testing.T) {
			s := replayCommandService()
			if err := s.VoiceCommand("replay", target); err == nil {
				t.Fatalf("accepted invalid target %q", target)
			}
			if len(s.voiceCommands) != 0 {
				t.Fatal("invalid replay was queued")
			}
		})
	}
}

func TestVoiceCommandReplayRejectsExpiredOrUncached(t *testing.T) {
	for _, tc := range []struct {
		name, target, want string
		turns              []uint32
	}{
		{"old session", "41:7", "session has expired", []uint32{7}},
		{"future session", "43:7", "session has expired", []uint32{7}},
		{"uncached turn", "42:8", "not cached", []uint32{3, 7}},
		{"empty cache", "42:7", "not cached", []uint32{}},
		{"nil cache", "42:7", "not cached", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := replayCommandService()
			s.state.Voice.ReplayTurns = tc.turns
			err := s.VoiceCommand("replay", tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if len(s.voiceCommands) != 0 {
				t.Fatal("unavailable replay was queued")
			}
		})
	}
}

func TestVoiceCommandReplayQueuesOnlyTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session uint64
		turn    uint32
	}{
		{"first cached turn", 42, 3},
		{"later cached turn", 42, 7},
		{"minimum identifiers", 1, 1},
		{"maximum identifiers", ^uint64(0), ^uint32(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := replayCommandService()
			s.state.Voice.Session = tc.session
			s.state.Voice.ReplayTurns = []uint32{3, tc.turn}
			target := strconv.FormatUint(tc.session, 10) + ":" + strconv.FormatUint(uint64(tc.turn), 10)
			if err := s.VoiceCommand("replay", target); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-s.voiceCommands:
				if want := (voice.Command{Kind: "replay", Turn: tc.turn}); got != want {
					t.Fatalf("queued %+v, want %+v", got, want)
				}
			default:
				t.Fatal("replay was not queued")
			}
		})
	}
}

func TestVoiceCommandReplayRespectsAvailability(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Service)
		want   error
		text   string
	}{
		{"closed", func(s *Service) { s.closed = true }, ErrClosed, ""},
		{"disabled", func(s *Service) { s.state.Settings.Voice.Enabled = false }, nil, "disabled"},
		{"offline", func(s *Service) { s.state.Voice.Connected = false }, ErrOffline, ""},
		{"missing queue", func(s *Service) { s.voiceCommands = nil }, ErrOffline, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := replayCommandService()
			tc.mutate(s)
			err := s.VoiceCommand("replay", "42:7")
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("error = %v, want %q", err, tc.text)
			}
			if len(s.voiceCommands) != 0 {
				t.Fatal("unavailable command was queued")
			}
		})
	}
}

func TestVoiceCommandReplayRejectsFullQueue(t *testing.T) {
	s := replayCommandService()
	for i := 0; i < cap(s.voiceCommands); i++ {
		s.voiceCommands <- voice.Command{Kind: "cancel"}
	}
	if err := s.VoiceCommand("replay", "42:7"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("full queue error = %v", err)
	}
	if len(s.voiceCommands) != cap(s.voiceCommands) {
		t.Fatal("full queue changed")
	}
	for len(s.voiceCommands) > 0 {
		if got := <-s.voiceCommands; got.Kind != "cancel" {
			t.Fatalf("full queue contents changed: %+v", got)
		}
	}
}
