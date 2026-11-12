package voice

import (
	"errors"
	"unicode/utf8"
)

// speechTextStream validates the bounded text stream without imposing speech
// boundaries. Only an incomplete UTF-8 code point waits for the next delta.
// Used before both UI updates and provider-specific TTS submission.
type speechTextStream struct {
	partial string
	total   int
	err     error
}

func (s *speechTextStream) Push(delta string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if len(delta) > maxSpeechTextBytes-s.total {
		s.err = errors.New("speech text exceeds 16384 bytes")
		return "", s.err
	}
	s.total += len(delta)
	raw := s.partial + delta
	complete := 0
	for complete < len(raw) {
		if !utf8.FullRuneInString(raw[complete:]) {
			break
		}
		r, n := utf8.DecodeRuneInString(raw[complete:])
		if r == utf8.RuneError && n == 1 {
			s.err = errors.New("speech text contains invalid UTF-8")
			return "", s.err
		}
		complete += n
	}
	s.partial = raw[complete:]
	return raw[:complete], nil
}

func (s *speechTextStream) Finish() error {
	if s.err == nil && s.partial != "" {
		s.err = errors.New("speech text ends with incomplete UTF-8")
	}
	return s.err
}
