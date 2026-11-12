package voice

import "time"

const responseWireInterval = 100 * time.Millisecond

// Only the LLM producer writes provisional responses. Replacing a snapshot is
// safe: every snapshot contains the entire visible reply, never an append delta.
func publishLatestResponse(mailbox chan result, r result) {
	select {
	case mailbox <- r:
		return
	default:
	}
	select {
	case <-mailbox:
	default:
	}
	mailbox <- r
}

// Drain before ordered results, including final response/error/EOF, so a queued
// partial snapshot can never regress a final response or cross turn completion.
func (s *session) flushResponse() error {
	select {
	case r := <-s.responses:
		return s.result(r)
	default:
		return nil
	}
}
