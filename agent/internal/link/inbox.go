package link

import (
	"encoding/json"
	"sync"
)

const otherFrameLimit = 16

type pendingFrame struct {
	state string // empty for best-effort frames
	data  []byte
}

// inbox decouples the WS reader from an optional, potentially slow consumer.
// At most one valid report of each state type and 16 other frames are pending.
// Replacement keeps a state's queue position so neither state can be starved.
// The dispatcher may additionally hold one obsolete snapshot while enqueue runs.
type inbox struct {
	mu       sync.Mutex
	pending  []*pendingFrame
	others   int
	incoming chan []byte
	wake     chan struct{}
	done     chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

func newInbox() *inbox {
	q := &inbox{
		pending:  make([]*pendingFrame, 0, otherFrameLimit+2),
		incoming: make(chan []byte),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go q.dispatch()
	return q
}

// Only complete, understood v1 reports may replace a valid pending state.
// Keep this validation aligned with desktop's device report handling: malformed
// or future-version frames must not erase the last usable state.
func stateKind(data []byte) string {
	var f struct {
		V          int    `json:"v"`
		Type       string `json:"type"`
		Page       string `json:"page"`
		On         *bool  `json:"on"`
		Brightness *int   `json:"brightness"`
	}
	if json.Unmarshal(data, &f) != nil || f.V != 1 {
		return ""
	}
	switch f.Type {
	case "page_state":
		if f.Page == "metrics" || f.Page == "quota" || f.Page == "voice" {
			return f.Type
		}
	case "screen_state":
		if f.On != nil && f.Brightness != nil {
			return f.Type
		}
	}
	return ""
}

func (q *inbox) enqueue(data []byte) {
	frame := &pendingFrame{state: stateKind(data), data: data}
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-q.done:
		return
	default:
	}
	if frame.state != "" {
		for i, old := range q.pending {
			if old.state == frame.state {
				q.pending[i] = frame
				q.notify()
				return
			}
		}
	} else {
		if q.others == otherFrameLimit {
			return
		}
		q.others++
	}
	q.pending = append(q.pending, frame)
	q.notify()
}

func (q *inbox) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *inbox) next() *pendingFrame {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	return q.pending[0]
}

func (q *inbox) sent(frame *pendingFrame) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// enqueue may have replaced this state while the dispatcher was sending.
	// Pointer identity is the revision: never delete its newer replacement.
	if len(q.pending) == 0 {
		return
	}
	if q.pending[0] != frame {
		// The old state was delivered, but its replacement still needs a turn.
		// Move that replacement behind other pending frames for fairness.
		latest := q.pending[0]
		copy(q.pending, q.pending[1:])
		q.pending[len(q.pending)-1] = latest
		return
	}
	if frame.state == "" {
		q.others--
	}
	copy(q.pending, q.pending[1:])
	q.pending[len(q.pending)-1] = nil
	q.pending = q.pending[:len(q.pending)-1]
}

func (q *inbox) stop() { q.stopOnce.Do(func() { close(q.done) }) }

func (q *inbox) dispatch() {
	defer close(q.stopped)
	defer close(q.incoming)
	defer func() {
		q.mu.Lock()
		q.pending = nil
		q.others = 0
		q.mu.Unlock()
	}()
	for {
		select {
		case <-q.done:
			return
		default:
		}
		frame := q.next()
		var outgoing chan []byte
		var data []byte
		if frame != nil {
			outgoing, data = q.incoming, frame.data
		}
		select {
		case <-q.done:
			return
		case <-q.wake:
			// Refresh a potentially replaced snapshot before blocking again.
		case outgoing <- data:
			q.sent(frame)
		}
	}
}
