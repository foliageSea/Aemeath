// Package holdkey reserves global keyboard shortcuts and reports hold transitions.
//
// Start, StartAlternate, StartShortcut, and their stop functions must be called
// on the MyGO main thread.
// Callbacks run serially on a package-owned goroutine, not the UI thread; marshal
// UI work to MyGO as needed. A callback must return promptly and must not wait
// for stop or synchronously wait for work on the main thread. Stop waits for any
// running callback and guarantees that no callback runs after it returns.
package holdkey

import (
	"errors"
	"sync"
)

// ErrUnsupported means this build has no native global shortcut implementation.
var ErrUnsupported = errors.New("global hold listener is unsupported on this platform (macOS requires cgo)")

// Start reserves F8 system-wide and reports true on press and false on release.
// Repeated key-down events are suppressed. Registration conflicts are returned
// as errors. The returned stop function releases the reservation and is
// idempotent. Stopping discards pending transitions; callers should clear any
// held application state when stopping. See the package documentation for
// threading requirements.
func Start(onChange func(down bool)) (stop func(), err error) {
	return StartShortcut("F8", onChange)
}

// StartAlternate reserves Ctrl+Shift+F8 instead of bare F8. It has the same
// threading and lifecycle requirements as Start.
func StartAlternate(onChange func(down bool)) (stop func(), err error) {
	return StartShortcut("Ctrl+Shift+F8", onChange)
}

// StartShortcut reserves a global shortcut and reports true on press and false
// on release. Syntax and supported keys are described by NormalizeShortcut.
// macOS supports F1-F20 and uses physical US ANSI letter/digit key positions;
// Windows supports F1-F24 and virtual letter/digit keys. OS-reserved shortcuts
// or existing registrations may fail. Lifecycle and threading match Start.
func StartShortcut(value string, onChange func(bool)) (func(), error) {
	if onChange == nil {
		return nil, errors.New("holdkey: onChange must not be nil")
	}
	s, err := parseShortcut(value)
	if err != nil {
		return nil, err
	}
	return start(onChange, s)
}

// transitions keeps native event handling independent of user callback latency.
// The mutex orders enqueue against shutdown, while done joins an active callback.
type transitions struct {
	mu       sync.Mutex
	ready    *sync.Cond
	queue    []bool
	down     bool
	stopped  bool
	callback func(bool)
	done     chan struct{}
}

func newTransitions(callback func(bool)) *transitions {
	t := &transitions{callback: callback, done: make(chan struct{})}
	t.ready = sync.NewCond(&t.mu)
	go t.run()
	return t
}

func (t *transitions) change(down bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.down == down {
		return
	}
	t.down = down
	t.queue = append(t.queue, down)
	t.ready.Signal()
}

func (t *transitions) run() {
	defer close(t.done)
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		for !t.stopped && len(t.queue) == 0 {
			t.ready.Wait()
		}
		if t.stopped {
			return
		}
		down := t.queue[0]
		t.queue = t.queue[1:]
		t.mu.Unlock()
		t.callback(down)
		t.mu.Lock()
	}
}

func (t *transitions) shutdown() {
	t.mu.Lock()
	t.stopped = true
	t.queue = nil
	t.ready.Broadcast()
	t.mu.Unlock()
	<-t.done
}
