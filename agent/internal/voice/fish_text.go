package voice

import (
	"context"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	fishTextMaxWait      = 80 * time.Millisecond
	fishTextClauseRunes  = 12
	fishTextTargetRunes  = 40
	streamTextQueueDepth = 8 // in-flight messages, NOT a per-reply segment limit
)

// forwardFishText batches tiny LLM deltas, not complete-WAV sentence groups.
// A short clause is submitted promptly; otherwise the oldest buffered text waits
// at most maxWait (excluding downstream backpressure). There is no segment cap.
// Whitespace is retained so English words split across deltas do not concatenate.
func forwardFishText(ctx context.Context, first string, parts <-chan string, out chan<- string, maxWait time.Duration) {
	defer close(out)
	pending := first
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	var timeout <-chan time.Time
	send := func(text string) bool {
		if text == "" {
			return true
		}
		select {
		case out <- text:
			return true
		case <-ctx.Done():
			return false
		}
	}
	drain := func() bool {
		for pending != "" {
			// Dialogue deltas are already validated; preview callers may pass raw
			// transport fragments. Leave these intact for the provider validator.
			if !utf8.ValidString(pending) {
				break
			}
			runes := []rune(pending)
			end := fishTextBoundary(runes)
			if end == 0 {
				break
			}
			bytes := 0
			for _, r := range runes[:end] {
				bytes += utf8.RuneLen(r)
			}
			if !send(pending[:bytes]) {
				return false
			}
			pending = pending[bytes:]
		}
		return true
	}
	for {
		if !drain() {
			return
		}
		if pending != "" && timeout == nil {
			timer.Reset(maxWait)
			timeout = timer.C
		} else if pending == "" && timeout != nil {
			timer.Stop()
			timeout = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			if !send(pending) {
				return
			}
			pending = ""
			timeout = nil
		case part, ok := <-parts:
			if !ok {
				send(pending)
				return
			}
			pending += part
		}
	}
}

// Fish has one continuous synthesis session: prefer short clauses on EVERY
// submission, rather than MiMo's first-short/later-long complete-WAV policy.
// Only lexical safety helpers are shared with the sentence segmenter.
func fishTextBoundary(text []rune) int {
	for i, r := range text {
		if i >= fishTextTargetRunes {
			break
		}
		strong, soft := speechPunctuation(text, i, false)
		if !strong && !soft {
			continue
		}
		end := i + 1
		for end < len(text) && speechClosing(text[end]) {
			end++
		}
		if end == len(text) && r != '\n' && r != '\r' && speechUnclosedQuote(text[:end]) {
			continue
		}
		if (strong && i > 0) || end >= fishTextClauseRunes {
			return end
		}
	}
	if len(text) <= fishTextTargetRunes {
		return 0
	}
	for end := fishTextTargetRunes; end >= fishTextClauseRunes; end-- {
		if unicode.IsSpace(text[end-1]) && speechSafeCut(text, end) {
			return end
		}
	}
	for end := fishTextTargetRunes; end >= fishTextClauseRunes; end-- {
		if speechSafeCut(text, end) {
			return end
		}
	}
	// An indivisible token is handled by the short-time flush, not length cutting.
	return 0
}
