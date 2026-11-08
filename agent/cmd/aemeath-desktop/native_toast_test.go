package main

import (
	"testing"
	"time"
)

func TestNativePageFeedbackUsesTransientToastWithoutMovingContent(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	f.mu.Lock()
	f.state.Running = true
	f.state.Connected = true
	f.state.Status = "connected"
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	before, ok := tt.Find("指标卡 CPU")
	if !ok {
		t.Fatal("CPU card missing")
	}
	show := func(label string) {
		t.Helper()
		nativeClick(t, tt, label)
		nativeComplete(t, updates, tt)
		tt.Advance(250 * time.Millisecond)
		if !tt.HasText("切页指令已发送") {
			t.Fatal("page feedback toast missing")
		}
		toast, ok := tt.Find("切页指令已发送")
		if !ok || toast.Y < 650 {
			t.Fatalf("feedback is not floating near the bottom: %+v", toast)
		}
		after, _ := tt.Find("指标卡 CPU")
		if after != before {
			t.Fatalf("toast moved content: before=%+v after=%+v", before, after)
		}
	}
	show("额度屏")
	tt.Advance(5 * time.Second)
	if tt.HasText("切页指令已发送") {
		t.Fatal("toast did not expire")
	}
	for range 3 {
		a.refresh()
		tt.Frame()
		tt.Advance(time.Second)
	}
	if tt.HasText("切页指令已发送") {
		t.Fatal("refresh redisplayed an expired toast")
	}
	show("性能屏")
}
