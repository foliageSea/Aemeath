package fishapi

import (
	"context"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestWriterTextOverflowKeepsRootCauseWithoutReplay(t *testing.T) {
	client, connections := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
		// The server never completes or rejects the session; the writer's own text
		// validation must be the cause of termination, not an ambiguous remote EOF.
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})
	const attempts = 100
	for attempt := 0; attempt < attempts; attempt++ {
		audioCalls := 0
		err := client.Stream(testContext(t), textInput(strings.Repeat("x", maxTextBytes+1)), func([]int16) error { audioCalls++; return nil })
		if err == nil || err.Error() != "Fish: text exceeds 16384 byte limit" {
			t.Fatalf("attempt %d: writer root cause lost: %v", attempt, err)
		}
		if audioCalls != 0 {
			t.Fatalf("attempt %d: unexpected audio callbacks=%d", attempt, audioCalls)
		}
		if got := connections.Load(); got != int32(attempt+1) {
			t.Fatalf("attempt %d: connections=%d; request replayed", attempt, got)
		}
	}
}
