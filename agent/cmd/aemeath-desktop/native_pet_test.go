package main

import (
	"context"
	"errors"
	"image"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/pet"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// These tests exercise the UI/controller boundary, not the pure engine's rules.
// No mygo.Window, application, tray, display server, or voice service is started.
type petTestWindow struct {
	bounds                            mygo.Rectangle
	boundsReads, positioned           int
	shown, hidden, invalidated, moved int
	top, ignore                       bool
	ignoreCalls                       []bool
}

func (w *petTestWindow) Bounds() mygo.Rectangle     { w.boundsReads++; return w.bounds }
func (w *petTestWindow) SetBounds(b mygo.Rectangle) { w.bounds, w.moved = b, w.moved+1 }
func (w *petTestWindow) ShowInactive()              { w.shown++ }
func (w *petTestWindow) Hide()                      { w.hidden++ }
func (w *petTestWindow) Invalidate()                { w.invalidated++ }
func (w *petTestWindow) SetAlwaysOnTop(on bool)     { w.top = on }
func (w *petTestWindow) SetIgnoreMouseEvents(on bool) {
	w.ignore = on
	w.ignoreCalls = append(w.ignoreCalls, on)
}

type petTestSave struct {
	path string
	cfg  pet.Config
}

type petTestHarness struct {
	p             *nativePet
	win, bubble   *petTestWindow
	service       *nativeFake
	now           time.Time
	cursor        mygo.Point
	displays      []mygo.Display
	screenReads   int
	saves         []petTestSave
	commands      chan [2]string
	updates       chan func()
	conversations int
}

func newPetTest(t *testing.T) *petTestHarness {
	t.Helper()
	cfg := pet.DefaultConfig()
	cfg.X, cfg.Y, cfg.PositionSet = 100, 220, true
	h := &petTestHarness{
		now:      time.Unix(1700000000, 0),
		cursor:   mygo.Point{X: 150, Y: 270},
		displays: []mygo.Display{{WorkArea: mygo.Rectangle{X: 0, Y: 0, Width: 1200, Height: 800}}},
		service:  &nativeFake{state: desktop.State{Settings: desktop.Settings{Address: "main-settings-must-not-change"}}},
		commands: make(chan [2]string, 16),
		updates:  make(chan func(), 16),
	}
	p := newPetState(cfg, h.now)
	h.p = p
	p.now = func() time.Time { return h.now }
	p.cursor = func() mygo.Point { return h.cursor }
	p.screens = func() []mygo.Display { h.screenReads++; return h.displays }
	p.snapshot = h.service.Snapshot
	p.command = func(command, text string) error { h.commands <- [2]string{command, text}; return nil }
	p.dispatch = func(fn func()) { h.updates <- fn }
	p.openConversation = func() { h.conversations++ }
	p.path = filepath.Join(t.TempDir(), "pet.json")
	p.save = func(path string, cfg pet.Config) error {
		h.saves = append(h.saves, petTestSave{path, cfg})
		return nil
	}
	p.canIgnoreMouse = true // A successfully installed fake tray provides recovery.
	p.updateScreens()
	h.win = &petTestWindow{bounds: mygo.Rectangle{X: 100, Y: 220, Width: p.state.Size.X, Height: p.state.Size.Y}}
	h.bubble = &petTestWindow{}
	p.win, p.bubble = h.win, h.bubble
	bitmap, err := ui.DecodeBitmap(pet.AtlasPNG)
	if err != nil {
		t.Fatal(err)
	}
	p.atlas = bitmap.Pixelated()
	p.refreshAssistant(h.now)
	p.applyVisibility()
	return h
}

func (h *petTestHarness) character() *ui.Tester {
	return ui.NewTester(h.p.view, h.p.state.Size.X, h.p.state.Size.Y)
}

func (h *petTestHarness) bubbleTester() *ui.Tester {
	return ui.NewTester(h.p.bubbleView, 300, 164)
}

func (h *petTestHarness) thinking() {
	h.service.state.Settings.Voice.Enabled = true
	h.service.state.Voice.Connected = true
	h.service.state.Voice.Session = 7
	h.service.state.Voice.Turn = 2
	h.service.state.Voice.Phase = "thinking"
	h.p.refreshAssistant(h.now)
}

func petTestClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func (h *petTestHarness) complete(t *testing.T) {
	t.Helper()
	select {
	case fn := <-h.updates:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("pet action did not dispatch completion")
	}
}

func (h *petTestHarness) noCommand(t *testing.T) {
	t.Helper()
	select {
	case command := <-h.commands:
		t.Fatalf("unexpected assistant command: %v", command)
	case <-time.After(20 * time.Millisecond):
		// Commands run asynchronously; give an erroneous command a chance to arrive.
	}
}

func petTestEntry(t *testing.T, entries []petMenuEntry, id string) petMenuEntry {
	t.Helper()
	var lookup func([]petMenuEntry) (petMenuEntry, bool)
	lookup = func(entries []petMenuEntry) (petMenuEntry, bool) {
		for _, entry := range entries {
			if entry.id == id {
				return entry, true
			}
			if entry, ok := lookup(entry.children); ok {
				return entry, true
			}
		}
		return petMenuEntry{}, false
	}
	if entry, ok := lookup(entries); ok {
		return entry
	}
	t.Fatalf("missing pet menu entry %q", id)
	return petMenuEntry{}
}

func petTestItem(t *testing.T, items []*mygo.MenuItem, id string) *mygo.MenuItem {
	t.Helper()
	var lookup func([]*mygo.MenuItem) *mygo.MenuItem
	lookup = func(items []*mygo.MenuItem) *mygo.MenuItem {
		for _, item := range items {
			if item.ID == id {
				return item
			}
			if child := lookup(item.Submenu); child != nil {
				return child
			}
		}
		return nil
	}
	if item := lookup(items); item != nil {
		return item
	}
	t.Fatalf("missing native pet menu item %q", id)
	return nil
}

func TestPetTransparentWindowOptions(t *testing.T) {
	cfg := pet.DefaultConfig()
	cfg.AlwaysOnTop = false
	v := pet.View{Position: pet.Point{X: -321.6, Y: 42.4}, Size: image.Pt(240, 260)}
	character := petWindowOptions(func(*ui.Context) {}, cfg, v)
	bubble := petBubbleOptions(func(*ui.Context) {})
	for name, options := range map[string]mygo.WindowOptions{"character": character, "bubble": bubble} {
		t.Run(name, func(t *testing.T) {
			if !options.Hidden || !options.Frameless || !options.Transparent || options.BackgroundColor != "#00000000" || !options.DisableShadow || !options.SkipTaskbar {
				t.Fatalf("not a hidden transparent frameless overlay: %+v", options)
			}
			if !options.DisableResize || !options.DisableMinimize || !options.DisableMaximize || !options.DisableFullScreen || options.Content == nil {
				t.Fatalf("overlay controls/content options: %+v", options)
			}
			if options.URL != "" {
				t.Fatalf("pet must use native content, not a second web UI: %q", options.URL)
			}
		})
	}
	if character.X != -322 || character.Y != 42 || character.Width != 240 || character.Height != 260 || character.AlwaysOnTop {
		t.Fatalf("character geometry/preferences not passed through: %+v", character)
	}
	if bubble.Width != 300 || bubble.Height != 164 || !bubble.AlwaysOnTop || bubble.Title == character.Title {
		t.Fatalf("bubble options: %+v", bubble)
	}
}

func TestPetCharacterSingleAndDoubleClickWithoutESP32(t *testing.T) {
	h := newPetTest(t)
	tt := h.character()
	if info, ok := tt.AccessByRole(ui.RoleButton, "爱弥斯桌宠"); !ok || info.Disabled {
		t.Fatal("character must remain locally interactive without a device")
	}
	petTestClick(t, tt, "爱弥斯桌宠")
	if h.p.state.Action != pet.Wave || h.p.pointerDown || h.p.dragging {
		t.Fatalf("single click did not wave/release: %+v", h.p.state)
	}
	tt.Advance(100 * time.Millisecond)
	h.now = h.now.Add(100 * time.Millisecond)
	r, _ := tt.Find("爱弥斯桌宠")
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Release(x, y)
	if h.p.state.Action != pet.Jump {
		t.Fatalf("double-click release overwrote jump: %+v", h.p.state)
	}
	if h.service.state.Connected || h.service.state.Voice.Connected || len(h.saves) != 0 || h.conversations != 0 {
		t.Fatal("local clicks unexpectedly connected, persisted, or opened the main UI")
	}
	h.noCommand(t)
}

func TestPetCharacterDragUsesScreenCoordinatesAndSavesOnRelease(t *testing.T) {
	h := newPetTest(t)
	tt := h.character()
	start, cursor := h.p.state.Position, h.cursor
	tt.Press(50, 50)
	if !h.p.pointerDown || h.p.dragging {
		t.Fatal("press must start tracking without immediately dragging")
	}
	h.cursor = mygo.Point{X: cursor.X + 3, Y: cursor.Y}
	tt.Move(52, 50)
	if h.p.dragging || h.p.state.Position != start {
		t.Fatal("sub-threshold motion moved the window")
	}
	h.cursor = mygo.Point{X: cursor.X + 4, Y: cursor.Y}
	tt.Move(54, 50)
	if !h.p.dragging || !h.p.state.Dragging || h.p.state.Position.X != start.X+4 {
		t.Fatalf("screen delta did not start dragging: %+v", h.p.state)
	}
	h.cursor = mygo.Point{X: cursor.X + 90, Y: cursor.Y + 35}
	tt.Move(70, 65)
	want := pet.Point{X: start.X + 90, Y: start.Y + 35}
	if h.p.state.Position != want {
		t.Fatalf("drag used local/accumulated delta: got %+v, want %+v", h.p.state.Position, want)
	}
	tt.Frame() // The moved native window changes local coordinates, not screen delta.
	if h.p.state.Position != want || len(h.saves) != 0 {
		t.Fatal("stationary screen cursor drifted or saved during drag")
	}
	tt.Release(70, 65)
	if h.p.pointerDown || h.p.dragging || h.p.state.Dragging || len(h.saves) != 1 {
		t.Fatalf("release did not end drag/save once: down=%v dragging=%v state=%+v saves=%v", h.p.pointerDown, h.p.dragging, h.p.state, h.saves)
	}
	if h.p.state.Action == pet.Wave || h.p.state.Action == pet.Jump {
		t.Fatalf("drag release was misinterpreted as a click: %+v", h.p.state)
	}
	saved := h.saves[0]
	if saved.path != h.p.path || !saved.cfg.PositionSet || saved.cfg.X != want.X || saved.cfg.Y != want.Y || h.win.bounds.X != int(want.X) || h.win.bounds.Y != int(want.Y) {
		t.Fatalf("drop did not persist actual window geometry: %+v, window=%+v", saved, h.win.bounds)
	}
	h.noCommand(t)
}

func TestPetCanceledDragRestoresPositionWithoutSaving(t *testing.T) {
	h := newPetTest(t)
	tt := h.character()
	start, cursor := h.p.state.Position, h.cursor
	tt.Press(50, 50)
	h.cursor = mygo.Point{X: cursor.X + 60, Y: cursor.Y + 30}
	tt.Move(75, 65)
	if !h.p.dragging || h.p.state.Position == start {
		t.Fatal("drag did not move before cancellation")
	}
	tt.CancelPress() // Focus loss/OS pointer cancellation is not a drop.
	h.p.render(h.now)
	if h.p.pointerDown || h.p.dragging || h.p.state.Dragging || h.p.state.Position != start || h.win.bounds.X != int(start.X) || h.win.bounds.Y != int(start.Y) {
		t.Fatalf("canceled drag was not restored: %+v window=%+v", h.p.state, h.win.bounds)
	}
	h.now = h.now.Add(3 * time.Second)
	h.p.render(h.now) // A canceled gesture must not leave a delayed persistence job.
	if len(h.saves) != 0 {
		t.Fatalf("canceled drag persisted preferences: %+v", h.saves)
	}
	tt.Advance(time.Second)
	petTestClick(t, tt, "爱弥斯桌宠")
	if h.p.state.Action != pet.Wave {
		t.Fatal("canceled drag left the next real click suppressed")
	}
	h.noCommand(t)
}

func TestPetBubbleFullConversationAndCancelUseExistingController(t *testing.T) {
	h := newPetTest(t)
	h.thinking() // Voice can be connected while the ESP32 metrics device is absent.
	if h.service.state.Connected || !h.p.bubbleShown || !h.p.bubbleCancel {
		t.Fatal("voice bubble incorrectly depends on an ESP32 device connection")
	}
	tt := h.bubbleTester()
	if !tt.HasText(h.p.bubbleText) {
		t.Fatal("projected assistant bubble text not rendered")
	}
	petTestClick(t, tt, "完整对话")
	if h.conversations != 1 {
		t.Fatal("full-conversation button did not open the existing main UI")
	}
	h.noCommand(t)
	petTestClick(t, tt, "取消当前请求")
	h.complete(t)
	if command := <-h.commands; command != [2]string{"cancel", ""} {
		t.Fatalf("unexpected voice command: %v", command)
	}
	if !strings.Contains(h.p.notice, "已发送取消请求") {
		t.Fatalf("cancel completion missing UI feedback: %q", h.p.notice)
	}
	h.noCommand(t)
}

func TestPetCancelRejectsChangedSnapshot(t *testing.T) {
	for _, change := range []string{"turn", "session", "disconnected"} {
		t.Run(change, func(t *testing.T) {
			h := newPetTest(t)
			h.thinking()
			tt := h.bubbleTester()
			switch change {
			case "turn":
				h.service.state.Voice.Turn++
			case "session":
				h.service.state.Voice.Session++
			case "disconnected":
				h.service.state.Voice.Connected = false
			}
			// No UI refresh yet: the old visible bubble must not cancel the new turn.
			petTestClick(t, tt, "取消当前请求")
			if !strings.Contains(h.p.notice, "当前对话已变化") {
				t.Fatalf("stale bubble was not rejected: %q", h.p.notice)
			}
			h.noCommand(t)
		})
	}
}

func TestPetOldContextActionCannotCancelRefreshedTurn(t *testing.T) {
	for _, change := range []string{"turn", "session"} {
		t.Run(change, func(t *testing.T) {
			h := newPetTest(t)
			h.thinking()
			// Also exercise identity binding in the native menu template conversion.
			items := nativePetMenuItems(h.p.menuEntries())
			oldCancel := petTestItem(t, items, "pet-cancel")
			if oldCancel.Disabled || oldCancel.Click == nil {
				t.Fatal("active turn must expose cancellation")
			}
			if change == "turn" {
				h.service.state.Voice.Turn++
			} else {
				h.service.state.Voice.Session++
			}
			h.p.refreshAssistant(h.now)
			oldCancel.Click(oldCancel, nil)
			if !strings.Contains(h.p.notice, "当前对话已变化") {
				t.Fatalf("old menu action followed the refreshed assistant identity: %q", h.p.notice)
			}
			h.noCommand(t)
			// A newly built action should still cancel that current turn normally.
			current := petTestItem(t, nativePetMenuItems(h.p.menuEntries()), "pet-cancel")
			current.Click(current, nil)
			h.complete(t)
			if got := <-h.commands; got != [2]string{"cancel", ""} {
				t.Fatal(got)
			}
		})
	}
}

func TestPetOpenHeadlessContextMenuCannotCancelRefreshedTurn(t *testing.T) {
	for _, change := range []string{"turn", "session"} {
		t.Run(change, func(t *testing.T) {
			h := newPetTest(t)
			h.thinking()
			tt := h.character()
			if err := tt.RightClick("爱弥斯桌宠"); err != nil {
				t.Fatal(err)
			}
			if change == "turn" {
				h.service.state.Voice.Turn++
			} else {
				h.service.state.Voice.Session++
			}
			h.p.refreshAssistant(h.now)
			if err := tt.ChooseMenuItem("取消当前请求"); err != nil {
				t.Fatal(err)
			}
			h.noCommand(t)
			// Reopening the menu must bind to the new identity and remain usable.
			if err := tt.RightClick("爱弥斯桌宠"); err != nil {
				t.Fatal(err)
			}
			if err := tt.ChooseMenuItem("取消当前请求"); err != nil {
				t.Fatal(err)
			}
			h.complete(t)
			if got := <-h.commands; got != [2]string{"cancel", ""} {
				t.Fatal(got)
			}
		})
	}
}

func TestPetCanceledContextMenuReopensForCurrentTurn(t *testing.T) {
	for _, initiallyActive := range []bool{false, true} {
		name := map[bool]string{false: "initially-disabled", true: "initially-active"}[initiallyActive]
		t.Run(name, func(t *testing.T) {
			h := newPetTest(t)
			if initiallyActive {
				h.thinking()
			}
			tt := h.character()
			if err := tt.RightClick("爱弥斯桌宠"); err != nil {
				t.Fatal(err)
			}
			tt.CloseMenu() // Equivalent to dismissing the system context menu with Escape.
			h.thinking()
			h.service.state.Voice.Turn++
			h.p.refreshAssistant(h.now)
			if err := tt.RightClick("爱弥斯桌宠"); err != nil {
				t.Fatal(err)
			}
			if err := tt.ChooseMenuItem("取消当前请求"); err != nil {
				t.Fatal(err)
			}
			h.complete(t)
			if got := <-h.commands; got != [2]string{"cancel", ""} {
				t.Fatal(got)
			}
		})
	}
}

func TestPetTrayCancelUsesCurrentTurnRatherThanStartupTurn(t *testing.T) {
	h := newPetTest(t)
	tray := h.p.trayItem() // Created while voice is disconnected, before any turn.
	cancel := petTestItem(t, tray.Submenu, "pet-cancel")
	h.thinking()
	cancel.Click(cancel, nil)
	h.complete(t)
	if got := <-h.commands; got != [2]string{"cancel", ""} {
		t.Fatal(got)
	}
}

func TestPetCancelFailureAndClosedCompletion(t *testing.T) {
	for _, closeBeforeCompletion := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "closed"}[closeBeforeCompletion], func(t *testing.T) {
			h := newPetTest(t)
			h.thinking()
			h.p.command = func(command, text string) error {
				h.commands <- [2]string{command, text}
				return errors.New("offline")
			}
			petTestClick(t, h.bubbleTester(), "取消当前请求")
			if closeBeforeCompletion {
				h.p.close()
			}
			before := h.bubble.invalidated
			h.complete(t)
			if got := <-h.commands; got != [2]string{"cancel", ""} {
				t.Fatal(got)
			}
			if closeBeforeCompletion {
				if h.p.notice != "" || h.bubble.invalidated != before {
					t.Fatal("late completion touched a closed pet")
				}
			} else if !strings.Contains(h.p.notice, "取消失败") {
				t.Fatalf("cancel failure was not surfaced: %q", h.p.notice)
			}
		})
	}
}

func TestPetPreferencesAreIndependentFromMainSettings(t *testing.T) {
	h := newPetTest(t)
	before := h.service.Snapshot()
	tt := h.character()
	choose := func(path ...string) {
		t.Helper()
		if err := tt.RightClick("爱弥斯桌宠"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(path...); err != nil {
			t.Fatal(err)
		}
	}
	choose("角色大小", "125%")
	choose("散步频率", "快")
	choose("始终置顶")
	if h.p.config.Scale != 1.25 || h.p.config.Frequency != pet.Fast || h.p.config.AlwaysOnTop || h.win.top || h.bubble.top {
		t.Fatalf("context preferences not applied to controller/windows: %+v", h.p.config)
	}
	if len(h.saves) != 3 {
		t.Fatalf("expected one pet save per preference action: %+v", h.saves)
	}
	for _, saved := range h.saves {
		if saved.path != h.p.path || filepath.Base(saved.path) != "pet.json" || !saved.cfg.PositionSet {
			t.Fatalf("preference used wrong persistence path/geometry: %+v", saved)
		}
	}
	if len(h.service.saves) != 0 || !reflect.DeepEqual(h.service.Snapshot(), before) {
		t.Fatal("pet preferences wrote or mutated the main service settings")
	}
	h.noCommand(t)
}

func TestPetScaleUpdatesWindowAndPersistsClampedBounds(t *testing.T) {
	h := newPetTest(t)
	h.displays = []mygo.Display{
		{WorkArea: mygo.Rectangle{X: -800, Y: -100, Width: 800, Height: 600}},
		{WorkArea: mygo.Rectangle{X: 1000, Y: 0, Width: 800, Height: 600}},
	}
	h.p.updateScreens()
	h.p.changeConfig(func(c *pet.Config) { c.X, c.Y = -192, 292 })
	h.saves = nil
	petTestEntry(t, h.p.menuEntries(), "pet-scale-1.5").action()
	want := mygo.Rectangle{X: -288, Y: 188, Width: 288, Height: 312}
	if h.win.bounds != want || h.p.state.Size != image.Pt(want.Width, want.Height) {
		t.Fatalf("scaled window escaped individual work area: got %+v, want %+v", h.win.bounds, want)
	}
	if len(h.saves) != 1 || h.saves[0].cfg.X != float64(want.X) || h.saves[0].cfg.Y != float64(want.Y) || h.saves[0].cfg.Scale != 1.5 {
		t.Fatalf("saved pre-clamp coordinates after scale: %+v", h.saves)
	}
}

func TestPetHiddenStopsAnimationDispatchAndInvalidation(t *testing.T) {
	h := newPetTest(t)
	h.thinking()
	ctx, cancel := context.WithCancel(context.Background())
	done := h.p.run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("pet animation loop did not stop")
		}
	})
	var queued func()
	select {
	case queued = <-h.updates:
	case <-time.After(2 * time.Second):
		t.Fatal("active pet never scheduled animation")
	}
	if !h.p.queued.Load() {
		t.Fatal("scheduled animation was not marked queued")
	}
	select {
	case <-h.updates:
		t.Fatal("animation dispatch grew while its previous frame was queued")
	case <-time.After(80 * time.Millisecond):
	}
	h.p.setVisible(false)
	if h.p.active.Load() || h.p.bubbleShown || h.win.hidden == 0 || h.bubble.hidden == 0 {
		t.Fatal("hiding did not deactivate and hide both windows")
	}
	winInvalidations, bubbleInvalidations := h.win.invalidated, h.bubble.invalidated
	h.now = h.now.Add(time.Second)
	queued() // A frame queued before hiding must not invalidate after hiding.
	if h.p.queued.Load() {
		t.Fatal("queued frame flag was not released")
	}
	h.p.render(h.now)
	h.service.state.Voice.Phase = "speaking"
	h.p.refreshAssistant(h.now)
	select {
	case <-h.updates:
		t.Fatal("hidden pet continued scheduling animation")
	case <-time.After(80 * time.Millisecond):
	}
	if h.win.invalidated != winInvalidations || h.bubble.invalidated != bubbleInvalidations {
		t.Fatal("hidden render/refresh/queued frame invalidated a window")
	}
}

func TestPetRecoverClearsMousePassThroughAndShowsClampedPet(t *testing.T) {
	h := newPetTest(t)
	h.p.setIgnoreMouse(true)
	if !h.p.ignoreMouse || !h.win.ignore || !h.bubble.ignore {
		t.Fatal("mouse pass-through was not applied to both windows")
	}
	h.p.setVisible(false)
	h.displays = []mygo.Display{{WorkArea: mygo.Rectangle{X: -600, Y: -400, Width: 600, Height: 400}}}
	h.p.recover()
	if h.p.ignoreMouse || h.win.ignore || h.bubble.ignore || !h.p.config.Visible || !h.p.active.Load() || h.win.shown < 2 {
		t.Fatal("recovery did not clear pass-through and restore visibility/activity")
	}
	for _, w := range []*petTestWindow{h.win, h.bubble} {
		if len(w.ignoreCalls) < 2 || w.ignoreCalls[len(w.ignoreCalls)-1] {
			t.Fatal("recovery did not explicitly restore mouse events")
		}
	}
	b := h.win.bounds
	if b.X < -600 || b.Y < -400 || b.X+b.Width > 0 || b.Y+b.Height > 0 {
		t.Fatalf("recovery left pet on a removed display: %+v", b)
	}
	h.noCommand(t)
}

func TestPetMousePassThroughRequiresRecoveryTray(t *testing.T) {
	h := newPetTest(t)
	h.p.canIgnoreMouse = false
	entry := petTestEntry(t, h.p.menuEntries(), "pet-ignore")
	if !entry.disabled {
		t.Fatal("pass-through must be unavailable without a recovery tray")
	}
	h.p.setIgnoreMouse(true)
	if h.p.ignoreMouse || h.win.ignore || h.bubble.ignore {
		t.Fatal("controller accepted unrecoverable mouse pass-through")
	}
	tt := h.character()
	if err := tt.RightClick("爱弥斯桌宠"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(entry.label); err == nil {
		t.Fatal("headless context menu enabled unavailable pass-through")
	}
	h.noCommand(t)
}

func TestPetBubbleTruncationLayoutAndCachedWorkArea(t *testing.T) {
	h := newPetTest(t)
	h.thinking()
	h.service.state.Voice.Phase = "speaking"
	h.service.state.Voice.Response = strings.Repeat("你", pet.BubbleRuneLimit+40)
	h.p.refreshAssistant(h.now)
	want := strings.Repeat("你", pet.BubbleRuneLimit-1) + "…"
	if h.p.bubbleText != want || utf8.RuneCountInString(h.p.bubbleText) != pet.BubbleRuneLimit {
		t.Fatalf("controller did not display the projected short reply: %q", h.p.bubbleText)
	}
	tt := h.bubbleTester()
	if !tt.HasText(want) || tt.HasText(h.service.state.Voice.Response) {
		t.Fatal("bubble rendered untruncated reply or omitted projected reply")
	}
	for _, label := range []string{"关闭气泡", "完整对话", "取消当前请求", want} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > 300 || r.Y+r.H > 164 {
			t.Fatalf("bubble content escaped the 300x164 window: %q %+v", label, r)
		}
	}
	if len([]rune(h.service.state.Voice.Response)) != pet.BubbleRuneLimit+40 {
		t.Fatal("projecting a short bubble mutated the full conversation")
	}
	reads, invalidations := h.screenReads, h.bubble.invalidated
	for range 5 {
		h.p.renderBubble(h.now)
	}
	if h.screenReads != reads || h.bubble.invalidated != invalidations {
		t.Fatal("unchanged bubble repeatedly queried screens or invalidated")
	}
}

func TestPetBubbleWindowBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pos   pet.Point
		areas []image.Rectangle
		want  mygo.Rectangle
	}{
		{"above", pet.Point{X: 400, Y: 400}, []image.Rectangle{image.Rect(0, 0, 1200, 800)}, mygo.Rectangle{X: 346, Y: 228, Width: 300, Height: 164}},
		{"below-near-top", pet.Point{X: 10, Y: 0}, []image.Rectangle{image.Rect(0, 0, 1200, 800)}, mygo.Rectangle{X: 0, Y: 216, Width: 300, Height: 164}},
		{"negative-monitor", pet.Point{X: -210, Y: -90}, []image.Rectangle{image.Rect(-1000, -100, 0, 600), image.Rect(1000, 0, 2000, 800)}, mygo.Rectangle{X: -300, Y: 126, Width: 300, Height: 164}},
		{"gap-uses-nearest-monitor", pet.Point{X: 950, Y: 400}, []image.Rectangle{image.Rect(-800, 0, 0, 800), image.Rect(1200, 0, 2000, 800)}, mygo.Rectangle{X: 1200, Y: 228, Width: 300, Height: 164}},
		{"tiny-work-area-anchors-origin", pet.Point{X: -90, Y: -70}, []image.Rectangle{image.Rect(-100, -80, 100, 20)}, mygo.Rectangle{X: -100, Y: -80, Width: 300, Height: 164}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := petBubbleBounds(tc.pos, image.Pt(192, 208), tc.areas); got != tc.want {
				t.Fatalf("bubble bounds = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPetBubbleDismissAndVisibilityLifecycle(t *testing.T) {
	h := newPetTest(t)
	h.thinking()
	tt := h.bubbleTester()
	petTestClick(t, tt, "关闭气泡")
	shown := h.bubble.shown
	h.p.render(h.now)
	if h.p.bubbleShown || h.bubble.shown != shown {
		t.Fatal("same dismissed bubble immediately returned")
	}
	h.service.state.Voice.Response = "这是新的回答"
	h.p.refreshAssistant(h.now)
	if !h.p.bubbleShown || h.p.bubbleText != "这是新的回答" || h.bubble.shown != shown+1 {
		t.Fatal("dismissing an earlier bubble suppressed a new response")
	}
	h.p.dragging = true
	h.p.renderBubble(h.now)
	if h.p.bubbleShown {
		t.Fatal("bubble remained visible while dragging")
	}
	h.p.dragging = false
	h.p.renderBubble(h.now)
	if !h.p.bubbleShown {
		t.Fatal("bubble did not return after dragging")
	}
	h.p.setIgnoreMouse(true)
	h.p.renderBubble(h.now)
	if h.p.bubbleShown {
		t.Fatal("click-through pet left an interactive bubble visible")
	}
	h.p.recover()
	if !h.p.bubbleShown {
		t.Fatal("recovering mouse interaction failed to restore current bubble")
	}
}

func TestPetMenuContentCheckedAndAvailability(t *testing.T) {
	h := newPetTest(t)
	entries := h.p.menuEntries()
	wantLabels := []string{"显示桌宠 / 恢复鼠标交互", "显示桌宠", "暂停自主活动", "允许桌面散步", "始终置顶", "鼠标穿透（从托盘恢复）", "角色大小", "散步频率", "-", "打开完整对话", "取消当前请求", "单击挥手 · 双击跳跃 · 拖动移动"}
	var labels []string
	for _, entry := range entries {
		label := entry.label
		if label == "" {
			label = "-"
		}
		labels = append(labels, label)
	}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Fatalf("pet menu contents = %v", labels)
	}
	items := nativePetMenuItems(entries)
	for id, checked := range map[string]bool{"pet-visible": true, "pet-paused": false, "pet-wander": true, "pet-top": true, "pet-ignore": false} {
		item := petTestItem(t, items, id)
		if item.Type != mygo.MenuItemCheckbox || item.Checked != checked || item.Disabled || item.Click == nil {
			t.Fatalf("checkbox %s: %+v", id, item)
		}
	}
	for _, id := range []string{"pet-scale-0.75", "pet-scale-1", "pet-scale-1.25", "pet-scale-1.5", "pet-frequency-slow", "pet-frequency-standard", "pet-frequency-fast"} {
		item := petTestItem(t, items, id)
		checked := id == "pet-scale-1" || id == "pet-frequency-standard"
		if item.Type != mygo.MenuItemRadio || item.Checked != checked || item.Disabled || item.Click == nil {
			t.Fatalf("radio %s: %+v", id, item)
		}
	}
	if !petTestItem(t, items, "pet-cancel").Disabled || !petTestItem(t, items, "pet-hint").Disabled || items[8].Type != mygo.MenuItemSeparator {
		t.Fatal("offline cancellation/hint/separator availability is wrong")
	}
	tt := h.character()
	if err := tt.RightClick("爱弥斯桌宠"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tt.Menu(), wantLabels) {
		t.Fatalf("headless context menu differs from shared entries: %v", tt.Menu())
	}
	if err := tt.ChooseMenuItem("取消当前请求"); err == nil {
		t.Fatal("offline menu permitted cancellation")
	}
	tt.CloseMenu()
	h.p.setPaused(true)
	h.p.setIgnoreMouse(true)
	h.thinking()
	entries = h.p.menuEntries()
	if !petTestEntry(t, entries, "pet-paused").checked || !petTestEntry(t, entries, "pet-ignore").checked || petTestEntry(t, entries, "pet-cancel").disabled {
		t.Fatal("menu did not project updated checked/cancel state")
	}
	h.noCommand(t)
}
