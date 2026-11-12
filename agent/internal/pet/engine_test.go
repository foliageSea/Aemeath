package pet

import (
	"image"
	"math"
	"testing"
	"time"
)

func newTestEngine() *Engine {
	e := NewEngine(DefaultConfig(), testNow)
	e.SetScreens([]image.Rectangle{image.Rect(0, 0, 1200, 800)})
	return e
}
func TestAnimationAtOriginalCadenceNotTickRate(t *testing.T) {
	e := newTestEngine()
	for i := 0; i < 30; i++ {
		now := testNow.Add(time.Duration(i) * time.Second / 30)
		v := e.Tick(now)
		want := int(now.Sub(testNow)/FrameDuration) % 6
		if v.Frame != want || v.Action != Idle || v.Rect != Frame(Idle, want) {
			t.Fatalf("tick %d: %+v want frame %d", i, v, want)
		}
	}
}
func TestClickDoubleClickAndPriority(t *testing.T) {
	e := newTestEngine()
	e.Click(testNow)
	if v := e.Tick(testNow); v.Action != Wave || v.Frame != 0 {
		t.Fatal(v)
	}
	e.DoubleClick(testNow.Add(100 * time.Millisecond))
	if v := e.Tick(testNow.Add(100 * time.Millisecond)); v.Action != Jump || v.Frame != 0 {
		t.Fatal(v)
	}
	e.SetAssistant(snapshot("thinking", 1), testNow.Add(200*time.Millisecond))
	if v := e.Tick(testNow.Add(200 * time.Millisecond)); v.Action != Review || !v.CanCancel {
		t.Fatal(v)
	}
	e.Click(testNow.Add(250 * time.Millisecond))
	if v := e.Tick(testNow.Add(250 * time.Millisecond)); v.Action != Review {
		t.Fatal("click overrode chat", v)
	}
	e.BeginDrag(testNow.Add(300 * time.Millisecond))
	e.DragTo(Point{100, 150})
	if v := e.Tick(testNow.Add(300 * time.Millisecond)); !v.Dragging || v.Action != Idle || v.Position != (Point{100, 150}) || v.Bubble != "" {
		t.Fatal("drag priority", v)
	}
	e.EndDrag(testNow.Add(400 * time.Millisecond))
	if v := e.Tick(testNow.Add(400 * time.Millisecond)); v.Action != Review {
		t.Fatal(v)
	}
	e.SetAssistant(snapshot("idle", 1), testNow.Add(500*time.Millisecond))
	if v := e.Tick(testNow.Add(500 * time.Millisecond)); v.Action != Idle {
		t.Fatal("queued lower-priority click replayed", v)
	}
	e.Click(testNow.Add(time.Second))
	e.Tick(testNow.Add(time.Second))
	if v := e.Tick(testNow.Add(time.Second + 4*FrameDuration)); v.Action != Idle {
		t.Fatal("click not one-shot", v)
	}
}
func TestMultiDisplayClampAndRemovedMonitor(t *testing.T) {
	screens := []image.Rectangle{image.Rect(-1600, -200, -100, 900), image.Rect(200, 0, 1400, 800)}
	size := image.Pt(192, 208)
	for _, tc := range []struct{ in, want Point }{{Point{-1500, -100}, Point{-1500, -100}}, {Point{-2000, -500}, Point{-1600, -200}}, {Point{0, 100}, Point{200, 100}}, {Point{-90, 850}, Point{-292, 692}}, {Point{2000, 1000}, Point{1208, 592}}} {
		if got := ClampPosition(tc.in, size, screens); got != tc.want {
			t.Fatalf("clamp %v=%v, want %v", tc.in, got, tc.want)
		}
	}
	if got := ClampPosition(Point{50, 50}, image.Pt(300, 400), []image.Rectangle{image.Rect(0, 0, 100, 100)}); got != (Point{0, 0}) {
		t.Fatal(got)
	}
	if got := ClampPosition(Point{math.NaN(), math.Inf(1)}, size, nil); got != (Point{}) {
		t.Fatal(got)
	}
	e := newTestEngine()
	e.SetScreens(screens)
	e.BeginDrag(testNow)
	e.DragTo(Point{-1500, -100})
	e.EndDrag(testNow)
	e.SetScreens(screens[1:])
	v := e.Tick(testNow)
	if v.Position != (Point{200, 0}) {
		t.Fatal("removed monitor stranded pet", v)
	}
	screens[1] = image.Rect(5000, 5000, 6000, 6000)
	if v = e.Tick(testNow); v.Position != (Point{200, 0}) {
		t.Fatal("screens not copied", v)
	}
}
func TestInitialPositionScaleAndPersistence(t *testing.T) {
	for _, scale := range []float64{.75, 1, 1.25, 1.5} {
		c := DefaultConfig()
		c.Scale = scale
		e := NewEngine(c, testNow)
		e.SetScreens([]image.Rectangle{image.Rect(0, 0, 1000, 800)})
		v := e.Tick(testNow)
		want := image.Pt(int(192*scale), int(208*scale))
		if v.Size != want || v.Position != (Point{float64(1000 - want.X - 24), float64(800 - want.Y - 24)}) {
			t.Fatal(v)
		}
	}
	c := DefaultConfig()
	c.PositionSet = true
	c.X = 0
	c.Y = 0
	e := NewEngine(c, testNow)
	e.SetScreens([]image.Rectangle{image.Rect(0, 0, 1000, 800)})
	if e.Tick(testNow).Position != (Point{}) {
		t.Fatal("explicit zero position ignored")
	}
	e.BeginDrag(testNow)
	e.DragTo(Point{50, 60})
	e.EndDrag(testNow)
	c = e.Config()
	if !c.PositionSet || c.X != 50 || c.Y != 60 {
		t.Fatal(c)
	}
	c.Scale = 1.5
	e.SetConfig(c)
	if v := e.Tick(testNow); v.Size != image.Pt(288, 312) || v.Position != (Point{50, 60}) {
		t.Fatal(v)
	}
}
func TestWanderingMovementPausedHoverAndNoTeleport(t *testing.T) {
	e := newTestEngine()
	e.autoAction = RunLeft
	e.autoUntil = testNow.Add(time.Minute)
	e.nextAuto = e.autoUntil
	before := e.position
	v := e.Tick(testNow.Add(100 * time.Millisecond))
	if v.Action != RunLeft || math.Abs((before.X-v.Position.X)-6.4) > 1e-8 {
		t.Fatal(v)
	}
	before = v.Position
	v = e.Tick(testNow.Add(20 * time.Second))
	if math.Abs((before.X-v.Position.X)-16) > 1e-8 {
		t.Fatal("stall teleported", v)
	}
	e.SetPaused(true)
	before = v.Position
	v = e.Tick(testNow.Add(21 * time.Second))
	if v.Action != Idle || v.Position != before {
		t.Fatal("pause did not stop", v)
	}
	e.Click(testNow.Add(21 * time.Second))
	if v = e.Tick(testNow.Add(21 * time.Second)); v.Action != Wave {
		t.Fatal("pause blocked click", v)
	}
	e.SetAssistant(snapshot("listening", 1), testNow.Add(22*time.Second))
	if v = e.Tick(testNow.Add(22 * time.Second)); v.Action != Wait {
		t.Fatal("pause blocked assistant", v)
	}
	e.SetAssistant(snapshot("idle", 1), testNow.Add(23*time.Second))
	e.SetPaused(false)
	e.SetHovered(true)
	e.autoAction = RunRight
	e.autoUntil = testNow.Add(time.Minute)
	if v = e.Tick(testNow.Add(24 * time.Second)); v.Action != Idle || v.Position != before {
		t.Fatal("hover did not stop", v)
	}
	e.SetHovered(false)
	c := e.Config()
	c.Wander = false
	e.SetConfig(c)
	e.autoAction = RunRight
	e.autoUntil = testNow.Add(time.Minute)
	if v = e.Tick(testNow.Add(25 * time.Second)); v.Action != Idle || v.Position != before {
		t.Fatal("wander false moved", v)
	}
}
func TestAutonomousActionsDeterministicAndFrequencyBounded(t *testing.T) {
	for _, f := range []Frequency{Slow, Standard, Fast} {
		c := DefaultConfig()
		c.Frequency = f
		a := NewEngine(c, testNow)
		b := NewEngine(c, testNow)
		a.SetScreens([]image.Rectangle{image.Rect(0, 0, 1000, 800)})
		b.SetScreens([]image.Rectangle{image.Rect(0, 0, 1000, 800)})
		for i := 0; i < 3000; i++ {
			now := testNow.Add(time.Duration(i) * time.Second / 30)
			av, bv := a.Tick(now), b.Tick(now)
			if av != bv {
				t.Fatal("nondeterministic", av, bv)
			}
			if av.Rect.Empty() || av.Frame < 0 || av.Frame >= frameCount(av.Action) {
				t.Fatal("invalid frame", av)
			}
		}
	}
	if !(activityInterval(Slow) > activityInterval(Standard) && activityInterval(Standard) > activityInterval(Fast)) {
		t.Fatal("frequency order")
	}
}
func TestBackwardClockDoesNotRegress(t *testing.T) {
	e := newTestEngine()
	first := e.Tick(testNow.Add(500 * time.Millisecond))
	back := e.Tick(testNow.Add(100 * time.Millisecond))
	if first != back {
		t.Fatal("clock regressed", first, back)
	}
	e.BeginDrag(testNow.Add(time.Second))
	e.DragTo(Point{math.NaN(), math.Inf(-1)})
	if v := e.Tick(testNow.Add(time.Second)); !finite(v.Position.X) || !finite(v.Position.Y) {
		t.Fatal(v)
	}
}
