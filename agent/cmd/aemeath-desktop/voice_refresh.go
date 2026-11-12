package main

import (
	"context"
	"time"
)

const voiceRefreshInterval = 50 * time.Millisecond

// runVoiceRefresh coalesces bursts within a fixed window (not a debounce, so
// continuous text cannot starve refresh). At most one UI callback is outstanding.
// Cancel also invalidates callbacks already queued on the UI thread.
func runVoiceRefresh(ctx context.Context, updates <-chan struct{}, dispatch func(func()), refresh func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
		}
		timer := time.NewTimer(voiceRefreshInterval)
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case _, ok := <-updates:
				if !ok {
					timer.Stop()
					return
				}
			case <-timer.C:
				break wait
			}
		}
		if ctx.Err() != nil {
			return
		}
		applied := make(chan struct{})
		dispatch(func() {
			defer close(applied)
			if ctx.Err() == nil {
				refresh()
			}
		})
		select {
		case <-ctx.Done():
			return
		case <-applied:
		}
	}
}
