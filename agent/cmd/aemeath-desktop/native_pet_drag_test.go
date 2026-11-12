package main

import (
	"aemeath/agent/internal/pet"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"testing"
	"time"
)

func TestPetPointerMovesWindowBeforeAnyFrameOrAnimationTick(t *testing.T) {
	h := newPetTest(t)
	h.p.moveWindow = func(b mygo.Rectangle) { h.win.bounds.X, h.win.bounds.Y = b.X, b.Y; h.win.positioned++ }
	h.p.render(h.now) // Establish the geometry cache once.
	reads, resizes := h.win.boundsReads, h.win.moved
	start, cursor := h.p.state.Position, h.cursor
	h.p.handlePointer(ui.InputEvent{Kind: ui.InputPointerDown, Button: 0})
	// All events retain the same local coordinates, as happens when the window
	// tracks the cursor. No Tester.Frame, clock advance, or animation dispatch.
	for i := 1; i <= 50; i++ {
		h.cursor = mygo.Point{X: cursor.X + 4*i, Y: cursor.Y + i}
		h.p.handlePointer(ui.InputEvent{Kind: ui.InputPointerMove, Button: 0, X: 50, Y: 50})
		want := pet.Point{X: start.X + float64(4*i), Y: start.Y + float64(i)}
		if h.p.state.Position != want || h.win.bounds.X != int(want.X) || h.win.bounds.Y != int(want.Y) {
			t.Fatalf("sample %d lagged: state=%+v bounds=%+v want=%+v", i, h.p.state, h.win.bounds, want)
		}
	}
	if h.win.positioned != 50 || h.win.moved != resizes || h.win.boundsReads != reads {
		t.Fatalf("drag resized/re-read bounds: moves=%d resizes=%d reads=%d", h.win.positioned, h.win.moved-resizes, h.win.boundsReads-reads)
	}
	moves := h.win.positioned
	h.p.handlePointer(ui.InputEvent{Kind: ui.InputPointerMove, Button: 0})
	if h.win.positioned != moves || len(h.saves) != 0 {
		t.Fatal("stationary pointer caused duplicate native motion or persistence")
	}
	// Releasing without another move must still include the final screen point.
	h.cursor.X += 7
	h.p.handlePointer(ui.InputEvent{Kind: ui.InputPointerUp, Button: 0})
	if h.win.bounds.X != int(start.X)+207 || len(h.saves) != 1 || h.p.dragging || h.p.pointerDown {
		t.Fatalf("final release lagged: bounds=%+v saves=%d", h.win.bounds, len(h.saves))
	}
}

func TestPetDragCancellationAfterSaveDeadlineDoesNotWrite(t *testing.T) {
	h := newPetTest(t)
	start := h.p.state.Position
	h.p.handlePointer(ui.InputEvent{Kind: ui.InputPointerDown, Button: 0})
	h.cursor.X += 70
	h.p.handlePointer(ui.InputEvent{Kind: ui.InputPointerMove, Button: 0})
	h.now = h.now.Add(3 * time.Second)
	h.p.render(h.now)
	h.p.handlePointer(ui.InputEvent{Kind: ui.InputPointerUp, Button: 0, Canceled: true})
	h.p.render(h.now.Add(3 * time.Second))
	if len(h.saves) != 0 || h.p.state.Position != start {
		t.Fatalf("cancellation wrote/moved: saves=%v position=%+v", h.saves, h.p.state.Position)
	}
}

func TestPetSizeChangesStillResizeInsteadOfPositionOnly(t *testing.T) {
	h := newPetTest(t)
	h.p.moveWindow = func(mygo.Rectangle) { t.Fatal("scale change used the position-only path") }
	h.p.render(h.now)
	before := h.win.moved
	h.p.changeConfig(func(c *pet.Config) { c.Scale = 1.5 })
	if h.win.moved != before+1 || h.win.bounds.Width != 288 || h.win.bounds.Height != 312 {
		t.Fatalf("resize did not update backing size: %+v", h.win.bounds)
	}
}
