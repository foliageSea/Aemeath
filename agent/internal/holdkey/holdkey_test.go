package holdkey

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestTransitionsPreserveOrderAndSuppressRepeat(t *testing.T) {
	got := make(chan bool, 16)
	events := newTransitions(func(down bool) { got <- down })
	defer events.shutdown()
	// Include initial releases, repeat presses, and rapid complete taps.
	for _, down := range []bool{false, true, true, true, false, false, true, false} {
		events.change(down)
	}
	want := []bool{true, false, true, false}
	received := make([]bool, 0, len(want))
	for range want {
		select {
		case down := <-got:
			received = append(received, down)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for transitions")
		}
	}
	events.shutdown()
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("transitions = %v, want %v", received, want)
	}
	select {
	case down := <-got:
		t.Fatalf("unexpected repeated transition: %v", down)
	default:
	}
}

func TestShutdownWaitsForCallbackAndDiscardsPendingEvents(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var got []bool
	events := newTransitions(func(down bool) {
		mu.Lock()
		got = append(got, down)
		mu.Unlock()
		close(entered)
		<-release
	})
	events.change(true)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not start")
	}
	events.change(false)
	stopped := make(chan struct{})
	go func() { events.shutdown(); close(stopped) }()
	// Observe the shutdown barrier deterministically while callback is blocked.
	barrier := make(chan struct{})
	go func() {
		events.mu.Lock()
		for !events.stopped {
			events.ready.Wait()
		}
		events.mu.Unlock()
		close(barrier)
	}()
	select {
	case <-barrier:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not establish barrier")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown returned while callback was still running")
	default:
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join callback")
	}
	events.change(true)
	events.change(false)
	events.shutdown() // Idempotent, including after native events race shutdown.
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, []bool{true}) {
		t.Fatalf("callbacks = %v, want only initial press", got)
	}
}

func TestStartRejectsNilCallback(t *testing.T) {
	stop, err := Start(nil)
	if err == nil || stop != nil {
		t.Fatalf("Start(nil) = (%v, %v), want nil stop and error", stop != nil, err)
	}
}
