package pet

import (
	"testing"
	"time"
)

func TestFailureOneShotDoesNotLockClicksOrExtendOnSnapshots(t *testing.T) {
	e := newTestEngine()
	s := snapshot("error", 1)
	s.Connected = false
	s.Error = "offline"
	e.SetAssistant(s, testNow)
	if v := e.Tick(testNow); v.Action != Failed || v.Bubble == "" || v.CanCancel {
		t.Fatal(v)
	}
	deadline := time.Duration(frameCount(Failed)) * ActionFrameDuration(Failed)
	for i := 1; i < 40; i++ {
		now := testNow.Add(time.Duration(i) * time.Second / 30)
		e.SetAssistant(s, now)
		v := e.Tick(now)
		if now.Before(testNow.Add(deadline)) && v.Action != Failed {
			t.Fatal("failure ended early", v)
		}
	}
	now := testNow.Add(deadline + time.Second)
	e.SetAssistant(s, now)
	if v := e.Tick(now); v.Action != Idle || v.Bubble == "" {
		t.Fatal("failure locked state or lost bounded error bubble", v)
	}
	e.Click(now)
	if v := e.Tick(now); v.Action != Wave {
		t.Fatal("retained service error blocked click", v)
	}
	e.SetAssistant(s, now.Add(time.Millisecond))
	if v := e.Tick(now.Add(time.Millisecond)); v.Action != Wave {
		t.Fatal("same error snapshot restarted failure", v)
	}
	now = testNow.Add(BubbleTTL + time.Second)
	s.Error = "changed diagnostic, same error phase"
	e.SetAssistant(s, now)
	if v := e.Tick(now); v.Action == Failed || v.Bubble != "" {
		t.Fatal("same-phase diagnostic refreshed reaction", v)
	}
	s.Turn = 2
	e.SetAssistant(s, now.Add(time.Second))
	if v := e.Tick(now.Add(time.Second)); v.Action != Failed {
		t.Fatal("new turn failure should react once", v)
	}
}

func TestFirmwareFrameCadence(t *testing.T) {
	e := newTestEngine()
	e.Click(testNow)
	if v := e.Tick(testNow.Add(139 * time.Millisecond)); v.Frame != 0 {
		t.Fatal(v)
	}
	if v := e.Tick(testNow.Add(140 * time.Millisecond)); v.Frame != 1 {
		t.Fatal("wave must use original 140ms", v)
	}
	e.DoubleClick(testNow.Add(time.Second))
	if v := e.Tick(testNow.Add(time.Second + 149*time.Millisecond)); v.Frame != 0 {
		t.Fatal(v)
	}
	if v := e.Tick(testNow.Add(time.Second + 150*time.Millisecond)); v.Frame != 1 {
		t.Fatal("jump must use original 150ms", v)
	}
	for _, r := range Rows {
		want := 150 * time.Millisecond
		if r.Action == Wave {
			want = 140 * time.Millisecond
		}
		if r.FrameDuration != want || ActionFrameDuration(r.Action) != want {
			t.Fatalf("firmware cadence %+v", r)
		}
	}
}

func TestDisableSessionZeroOverridesActiveSession(t *testing.T) {
	var p Projector
	s := snapshot("thinking", 5)
	s.Session = 9
	p.Update(s, testNow)
	if !p.View(testNow).CanCancel {
		t.Fatal("fixture not active")
	}
	p.Update(AssistantSnapshot{Phase: "disabled", Enabled: false}, testNow.Add(time.Second))
	if v := p.View(testNow.Add(time.Second)); v.Action != Idle || v.Active || v.CanCancel || v.Bubble != "" {
		t.Fatal("global disable ignored as stale session", v)
	}
	// Re-enabling cannot replay a delayed response from an older session.
	old := snapshot("speaking", 99)
	old.Reply = "old"
	p.Update(old, testNow.Add(2*time.Second))
	if v := p.View(testNow.Add(2 * time.Second)); v.Bubble != "" || v.CanCancel {
		t.Fatal("old session revived on re-enable", v)
	}
	s.Session = 10
	s.Turn = 1
	s.Reply = ""
	p.Update(s, testNow.Add(3*time.Second))
	if !p.View(testNow.Add(3 * time.Second)).CanCancel {
		t.Fatal("new enabled session rejected")
	}
}
func TestClickRestartsAtEventTimeAndDoesNotQueueUnderChat(t *testing.T) {
	e := newTestEngine()
	e.Click(testNow)
	if v := e.Tick(testNow.Add(2 * FrameDuration)); v.Frame != 2 || v.Action != Wave {
		t.Fatal("animation began at Tick rather than event", v)
	}
	now := testNow.Add(3 * FrameDuration)
	e.Click(now)
	if v := e.Tick(now); v.Frame != 0 || v.Action != Wave {
		t.Fatal("second click did not restart", v)
	}
	e.SetAssistant(snapshot("thinking", 1), now.Add(time.Millisecond))
	e.SetAssistant(snapshot("idle", 1), now.Add(2*time.Millisecond))
	if v := e.Tick(now.Add(3 * time.Millisecond)); v.Action != Idle {
		t.Fatal("preempted click replayed between ticks", v)
	}
}
