package link

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not terminate")
	}
}

func testInbox(t *testing.T) *inbox {
	t.Helper()
	q := newInbox()
	t.Cleanup(func() {
		q.stop()
		waitDone(t, q.stopped)
	})
	return q
}

func receiveLatest(t *testing.T, incoming <-chan []byte, page, screen string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gotPage, gotScreen := false, false
	for n := 0; !gotPage || !gotScreen; n++ {
		// There can be 18 pending entries and one obsolete dispatcher snapshot.
		if n == otherFrameLimit+3 {
			t.Fatal("latest reports were not delivered within the bounded backlog")
		}
		select {
		case data, ok := <-incoming:
			if !ok {
				t.Fatal("Incoming closed before latest reports")
			}
			gotPage = gotPage || string(data) == page
			gotScreen = gotScreen || string(data) == screen
		case <-ctx.Done():
			t.Fatalf("latest reports missing: page=%v screen=%v", gotPage, gotScreen)
		}
	}
}

func TestInboxLatestStatesSurviveFloodAndSlowConsumer(t *testing.T) {
	q := testInbox(t)
	if cap(q.incoming) != 0 {
		t.Fatal("Incoming must not retain an independently stale buffer")
	}
	// Fill the best-effort allowance before the first state arrives.
	for i := 0; i < otherFrameLimit+32; i++ {
		q.enqueue([]byte(`{"type":"unknown"}`))
	}
	// No consumer during each flood. Draining between rounds represents a
	// consumer that pauses while forwarding an outgoing network command.
	for round := 0; round < 3; round++ {
		page := fmt.Sprintf(`{"v":1,"type":"page_state","page":"voice","round":%d}`, round)
		screen := fmt.Sprintf(`{"v":1,"type":"screen_state","on":false,"brightness":42,"round":%d}`, round)
		produced := make(chan struct{})
		go func() {
			defer close(produced)
			for i := 0; i < 128; i++ {
				q.enqueue([]byte(fmt.Sprintf(`{"type":"unknown","n":%d}`, i)))
				q.enqueue([]byte(`{"v":1,"type":"page_state","page":"metrics"}`))
				q.enqueue([]byte(`{"v":1,"type":"screen_state","on":true,"brightness":99}`))
			}
			q.enqueue([]byte(page))
			q.enqueue([]byte(screen))
			// Neither invalid state lookalikes nor arbitrary frames may evict
			// the unique usable final state of either kind.
			for i := 0; i < 128; i++ {
				q.enqueue([]byte(`{"v":2,"type":"page_state","page":"quota"}`))
				q.enqueue([]byte(`{"v":1,"type":"screen_state","on":true}`))
				q.enqueue([]byte(`{"type":"unknown"}`))
			}
		}()
		waitDone(t, produced)
		q.mu.Lock()
		pending, capacity, others := len(q.pending), cap(q.pending), q.others
		q.mu.Unlock()
		if pending != otherFrameLimit+2 || capacity != otherFrameLimit+2 || others != otherFrameLimit {
			t.Fatalf("unbounded/incorrect pending: len=%d cap=%d others=%d", pending, capacity, others)
		}
		receiveLatest(t, q.incoming, page, screen)
	}
}

func TestInboxSentOldSnapshotDoesNotDeleteReplacement(t *testing.T) {
	// Drive the send/ack interleaving directly, with no scheduling assumptions:
	// dispatcher snapshots old, reader replaces it, old send completes, then ack.
	q := &inbox{wake: make(chan struct{}, 1), done: make(chan struct{})}
	q.enqueue([]byte(`{"v":1,"type":"page_state","page":"metrics"}`))
	old := q.next()
	q.enqueue([]byte(`{"v":1,"type":"page_state","page":"voice"}`))
	q.enqueue([]byte(`{"v":1,"type":"screen_state","on":false,"brightness":42}`))
	q.sent(old)
	if len(q.pending) != 2 {
		t.Fatal("acknowledging an old send removed the replacement")
	}
	if next := q.next(); next.state != "screen_state" {
		t.Fatal("replacement starved the other pending state")
	} else {
		q.sent(next)
	}
	if next := q.next(); next == old || string(next.data) != `{"v":1,"type":"page_state","page":"voice"}` {
		t.Fatalf("lost replacement: %+v", next)
	} else {
		q.sent(next)
	}
	if q.next() != nil {
		t.Fatal("acknowledged frame remained pending")
	}
}

func TestInboxStopWithoutConsumer(t *testing.T) {
	q := testInbox(t)
	for i := 0; i < 128; i++ {
		q.enqueue([]byte(`{"type":"unknown"}`))
	}
	q.enqueue([]byte(`{"v":1,"type":"page_state","page":"voice"}`))
	q.stop()
	q.stop() // idempotent cancellation
	waitDone(t, q.stopped)
	q.enqueue([]byte(`{"v":1,"type":"page_state","page":"quota"}`))
	if _, ok := <-q.incoming; ok {
		t.Fatal("Incoming remained open after dispatcher stopped")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) != 0 {
		t.Fatal("stopped inbox retained pending data")
	}
}

func TestStateKindProtectsOnlyValidV1Reports(t *testing.T) {
	for _, tt := range []struct{ data, want string }{
		{`{"v":1,"type":"page_state","page":"metrics"}`, "page_state"},
		{`{"v":1,"type":"page_state","page":"quota"}`, "page_state"},
		{`{"v":1,"type":"page_state","page":"voice"}`, "page_state"},
		{`{"v":1,"type":"screen_state","on":false,"brightness":250}`, "screen_state"},
		{`{"v":2,"type":"page_state","page":"voice"}`, ""},
		{`{"type":"page_state","page":"voice"}`, ""},
		{`{"v":1,"type":"page_state","page":"unknown"}`, ""},
		{`{"v":1,"type":"page_state","page":null}`, ""},
		{`{"v":1,"type":"page_state","page":1}`, ""},
		{`{"v":1,"type":"screen_state","on":true}`, ""},
		{`{"v":1,"type":"screen_state","on":null,"brightness":42}`, ""},
		{`{"v":1,"type":"screen_state","on":true,"brightness":1.5}`, ""},
		{`{"v":1,"type":"screen_state","on":true,"brightness":null}`, ""},
		{`{"v":1,"type":"unknown"}`, ""},
		{`not json`, ""},
	} {
		t.Run(tt.data, func(t *testing.T) {
			if got := stateKind([]byte(tt.data)); got != tt.want {
				t.Fatalf("stateKind=%q want %q", got, tt.want)
			}
		})
	}
}
