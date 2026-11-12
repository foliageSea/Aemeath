package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/voice"
)

func awaitVoiceRefresh(t *testing.T, queued <-chan func()) func() {
	t.Helper()
	select {
	case f := <-queued:
		return f
	case <-time.After(time.Second):
		t.Fatal("voice refresh did not arrive")
		return nil
	}
}

func TestVoiceRefreshCoalescesAndReadsLatestState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan struct{}, 1000)
	queued := make(chan func(), 2)
	done := make(chan struct{})
	var latest, shown atomic.Int64
	go func() {
		defer close(done)
		runVoiceRefresh(ctx, updates, func(f func()) { queued <- f }, func() { shown.Store(latest.Load()) })
	}()
	for i := int64(1); i <= 1000; i++ {
		latest.Store(i)
		updates <- struct{}{}
	}
	f := awaitVoiceRefresh(t, queued)
	latest.Store(1001)
	f()
	if shown.Load() != 1001 {
		t.Fatal("queued callback retained stale snapshot")
	}
	select {
	case <-queued:
		t.Fatal("burst generated multiple callbacks")
	case <-time.After(2 * voiceRefreshInterval):
	}
	cancel()
	<-done
}

func TestVoiceRefreshBoundedQueueAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan struct{}, 1000)
	queued := make(chan func(), 10)
	done := make(chan struct{})
	var rendered atomic.Int32
	go func() {
		defer close(done)
		runVoiceRefresh(ctx, updates, func(f func()) { queued <- f }, func() { rendered.Add(1) })
	}()
	updates <- struct{}{}
	f := awaitVoiceRefresh(t, queued)
	for i := 0; i < 1000; i++ {
		updates <- struct{}{}
	}
	select {
	case <-queued:
		t.Fatal("more than one callback queued")
	case <-time.After(2 * voiceRefreshInterval):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel waited for UI callback")
	}
	f()
	if rendered.Load() != 0 {
		t.Fatal("queued callback rendered after cancellation")
	}
}

func TestVoiceRefreshContinuousUpdatesDoNotStarve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan struct{}, 1)
	queued := make(chan func(), 1)
	done := make(chan struct{})
	go func() { defer close(done); runVoiceRefresh(ctx, updates, func(f func()) { queued <- f }, func() {}) }()
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				select {
				case updates <- struct{}{}:
				default:
				}
			}
		}
	}()
	awaitVoiceRefresh(t, queued)()
	awaitVoiceRefresh(t, queued)()
	cancel()
	<-done
	<-producerDone
}

func TestVoiceRefreshCanceledBeforeFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan struct{}, 1)
	updates <- struct{}{}
	cancel()
	runVoiceRefresh(ctx, updates, func(func()) { t.Fatal("canceled refresh scheduled redraw") }, func() {})
}

func TestVoiceRefreshStopsOnClosedStream(t *testing.T) {
	updates := make(chan struct{})
	close(updates)
	runVoiceRefresh(context.Background(), updates, func(func()) { t.Fatal("closed stream scheduled redraw") }, func() {})
}

func TestRefreshVoiceDoesNotSampleMetricsOrLogin(t *testing.T) {
	a, f, _, _ := nativeTest(t)
	a.draft.Address = "unsaved"
	a.loginRead = func() bool { t.Fatal("voice refresh queried native login state"); return false }
	before := len(a.history)
	f.mu.Lock()
	f.state.Voice = voice.State{Turn: 1, Transcript: "hello", Response: "streamed", Phase: "thinking"}
	f.mu.Unlock()
	a.refreshVoice()
	if a.snapshot.Voice.Response != "streamed" || len(a.voiceHistory) != 1 {
		t.Fatal("voice state did not refresh")
	}
	if len(a.history) != before || a.draft.Address != "unsaved" {
		t.Fatal("voice refresh changed metrics or draft")
	}
}
