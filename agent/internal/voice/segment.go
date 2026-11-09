package voice

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSpeechSegments  = 8
	maxSpeechTextBytes = 16 * 1024
)

// sentenceSegmenter preserves the exact input. The first clause starts speech
// promptly; subsequent clauses are larger to amortize complete-WAV requests.
// One segment slot is always reserved for the final, unsplit remainder.
// The zero value is ready to use. It is owned by one streaming callback.
type sentenceSegmenter struct {
	pending    string
	partial    string // an incomplete UTF-8 code point across transport deltas
	inputBytes int
	emitted    int
	finished   bool
	err        error
}

func (s *sentenceSegmenter) Push(delta string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.finished {
		return nil, errors.New("speech segmenter already finished")
	}
	if len(delta) > maxSpeechTextBytes-s.inputBytes {
		s.err = errors.New("speech text exceeds 16384 bytes")
		return nil, s.err
	}
	raw := s.partial + delta
	complete := 0
	for complete < len(raw) {
		if !utf8.FullRuneInString(raw[complete:]) {
			break
		}
		r, n := utf8.DecodeRuneInString(raw[complete:])
		if r == utf8.RuneError && n == 1 {
			s.err = errors.New("speech text contains invalid UTF-8")
			return nil, s.err
		}
		complete += n
	}
	s.inputBytes += len(delta)
	s.pending += raw[:complete]
	s.partial = raw[complete:]
	return s.drain(false), nil
}

// Finish flushes every remaining byte, including whitespace. Repeated Finish
// calls are harmless. Incomplete or invalid UTF-8 is an explicit terminal error.
func (s *sentenceSegmenter) Finish() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.finished {
		return nil, nil
	}
	if s.partial != "" {
		s.err = errors.New("speech text ends with incomplete UTF-8")
		return nil, s.err
	}
	s.finished = true
	out := s.drain(true)
	if s.pending != "" {
		out = append(out, s.pending)
		s.pending = ""
		s.emitted++
	}
	return out, nil
}

func (s *sentenceSegmenter) drain(final bool) []string {
	var out []string
	for s.pending != "" && s.emitted < maxSpeechSegments-1 {
		runes := []rune(s.pending)
		end := speechBoundary(runes, s.emitted == 0, final)
		if end == 0 {
			break
		}
		// Conversion through rune offsets cannot cut a UTF-8 code point. Slice the
		// original string to retain every byte without normalizing user text.
		bytes := 0
		for _, r := range runes[:end] {
			bytes += utf8.RuneLen(r)
		}
		out = append(out, s.pending[:bytes])
		s.pending = s.pending[bytes:]
		s.emitted++
	}
	return out
}

func speechBoundary(text []rune, first, final bool) int {
	minimum, maximum := 60, 100
	if first {
		minimum, maximum = 20, 40
	}
	content := 0
	for i, r := range text {
		if i >= maximum {
			break
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSymbol(r) {
			content++
		}
		strong, soft := speechPunctuation(text, i, final)
		if !strong && !soft {
			continue
		}
		end := i + 1
		// Attach closing quotes and punctuation to the sentence they terminate.
		// Numeric periods need lookahead; unquoted Chinese endings emit promptly.
		for end < len(text) && speechClosing(text[end]) {
			end++
		}
		if end == len(text) && !final && r != '\n' && r != '\r' && speechUnclosedQuote(text[:end]) {
			continue
		}
		if (strong && first && content >= 2) || end >= minimum {
			return end
		}
	}
	if len(text) <= maximum {
		return 0
	}
	// Prefer a nearby whitespace boundary for English. Never split decimal or
	// version tokens simply because they cross the preferred character budget.
	for end := maximum; end >= minimum; end-- {
		if unicode.IsSpace(text[end-1]) && speechSafeCut(text, end) {
			return end
		}
	}
	for end := maximum; end >= minimum; end-- {
		if speechSafeCut(text, end) {
			return end
		}
	}
	// A single long token is indivisible; wait for its end instead of damaging it.
	for end := maximum + 1; end < len(text); end++ {
		if speechSafeCut(text, end) {
			return end
		}
	}
	if final {
		return len(text)
	}
	return 0
}

func speechPunctuation(text []rune, i int, final bool) (strong, soft bool) {
	r := text[i]
	switch r {
	case '。', '！', '？', '!', '?', '\n', '\r':
		return true, false
	case '，', ',', '；', ';', '：', ':':
		// Keep numeric grouping and time values together as well as decimals.
		if i > 0 && i+1 < len(text) && unicode.IsDigit(text[i-1]) && unicode.IsDigit(text[i+1]) {
			return false, false
		}
		if i+1 == len(text) && !final && i > 0 && unicode.IsDigit(text[i-1]) {
			return false, false
		}
		return false, true
	case '.':
		if i+1 == len(text) && !final {
			return false, false
		}
		if i > 0 && i+1 < len(text) {
			if unicode.IsDigit(text[i-1]) && unicode.IsDigit(text[i+1]) {
				return false, false
			}
			if unicode.IsLetter(text[i-1]) && unicode.IsLetter(text[i+1]) {
				return false, false
			}
		}
		// Common English titles and abbreviations are not sentence endings.
		start := i
		for start > 0 && unicode.IsLetter(text[start-1]) {
			start--
		}
		switch strings.ToLower(string(text[start:i])) {
		case "mr", "mrs", "ms", "dr", "prof", "sr", "jr", "vs", "etc", "e", "g", "i":
			return false, false
		}
		return true, false
	}
	return false, false
}

func speechUnclosedQuote(text []rune) bool {
	depth := 0
	asciiQuote := false
	for _, r := range text {
		switch r {
		case '“', '‘', '「', '『', '«':
			depth++
		case '”', '’', '」', '』', '»':
			if depth > 0 {
				depth--
			}
		case '"':
			asciiQuote = !asciiQuote
		}
	}
	return depth > 0 || asciiQuote
}

func speechClosing(r rune) bool {
	return strings.ContainsRune("。！？!?….,，;；:：\"'”’」』）)]}】》〉»", r)
}
func speechSafeCut(text []rune, end int) bool {
	if end <= 0 || end >= len(text) {
		return false
	}
	left, right := text[end-1], text[end]
	if speechClosing(right) || unicode.IsMark(right) || right == '‍' || left == '‍' {
		return false
	}
	atom := func(r rune) bool {
		return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-$", r))
	}
	return !(atom(left) && atom(right))
}
