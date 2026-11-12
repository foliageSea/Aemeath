package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"math"
	"path/filepath"
	"sync/atomic"
	"time"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/pet"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The pet is a second view of the existing service, never a second assistant.
// All mutable state belongs to the main thread, including engine and menu state.
// Populated only by explicit e2e-tagged builds; absent in production.
var nativePetTestSetup func(*nativePet)

type petWindow interface {
	Bounds() mygo.Rectangle
	SetBounds(mygo.Rectangle)
	ShowInactive()
	Hide()
	Invalidate()
	SetAlwaysOnTop(bool)
	SetIgnoreMouseEvents(bool)
}

type nativePet struct {
	config           pet.Config
	engine           *pet.Engine
	state            pet.View
	path             string
	save             func(string, pet.Config) error
	persist          bool
	win, bubble      petWindow
	moveWindow       func(mygo.Rectangle)
	nativePointer    bool
	windowBounds     mygo.Rectangle
	windowBoundsSet  bool
	atlas            *ui.Bitmap
	snapshot         func() desktop.State
	command          func(string, string) error
	openConversation func()
	cursor           func() mygo.Point
	screens          func() []mygo.Display
	workAreas        []image.Rectangle
	canIgnoreMouse   bool
	assistantReady   bool
	now              func() time.Time
	dispatch         func(func())
	active           atomic.Bool
	queued           atomic.Bool
	closed           bool
	ignoreMouse      bool
	paused           bool
	pointerDown      bool
	suppressClick    bool
	contextEntries   []petMenuEntry
	dragging         bool
	pressCursor      mygo.Point
	pressPosition    pet.Point
	pressDirty       bool
	pressSaveAt      time.Time
	assistant        pet.AssistantSnapshot
	bubbleText       string
	bubbleCancel     bool
	bubbleShown      bool
	dismissed        string
	notice           string
	noticeUntil      time.Time
	dirty            bool
	saveAt           time.Time
	menu             *mygo.Menu
	offScreens       func()
}

func newNativePet(dir string, service *desktop.Service, open func()) (*nativePet, error) {
	path := filepath.Join(dir, "pet.json")
	cfg, err := pet.LoadConfig(path)
	if err != nil {
		log.Printf("pet: cannot load preferences: %v", err)
	}
	p := newPetState(cfg, time.Now())
	p.path, p.persist = path, err == nil
	p.save = pet.SaveConfig
	p.snapshot, p.command, p.openConversation = service.Snapshot, service.VoiceCommand, open
	p.cursor, p.screens, p.dispatch = mygo.Screen.CursorScreenPoint, mygo.Screen.Displays, mygo.RunOnMain
	p.updateScreens()
	p.refreshAssistant(time.Now()) // Establish baseline before exposing any historical reply.
	bitmap, err := ui.DecodeBitmap(pet.AtlasPNG)
	if err != nil {
		return nil, fmt.Errorf("pet atlas: %w", err)
	}
	p.atlas = bitmap.Pixelated()
	w := mygo.NewWindow(petWindowOptions(p.view, p.config, p.state))
	p.win = w
	p.moveWindow = petWindowMover(w)
	if err := configurePetWindow(w); err != nil {
		w.Destroy()
		return nil, err
	}
	p.nativePointer = connectPetPointer(w, p)
	bw := mygo.NewWindow(petBubbleOptions(p.bubbleView))
	p.bubble = bw
	if err := configurePetWindow(bw); err != nil {
		bw.Destroy()
		w.Destroy()
		return nil, err
	}
	w.OnClose(func(e *mygo.CloseEvent) {
		if !p.closed {
			e.PreventDefault()
			p.setVisible(false)
		}
	})
	bw.OnClose(func(e *mygo.CloseEvent) {
		if !p.closed {
			e.PreventDefault()
			p.dismissBubble()
		}
	})
	p.offScreens = mygo.Screen.OnDisplaysChanged(func() {
		if !p.closed {
			p.updateScreens()
			p.render(time.Now())
			p.markDirty()
		}
	})
	p.engine.Click(p.now())
	p.state = p.engine.Tick(p.now())
	p.applyVisibility()
	if nativePetTestSetup != nil {
		nativePetTestSetup(p)
	}
	return p, nil
}

func newPetState(cfg pet.Config, now time.Time) *nativePet {
	cfg = pet.NormalizeConfig(cfg)
	e := pet.NewEngine(cfg, now)
	p := &nativePet{config: cfg, engine: e, now: time.Now, persist: true}
	p.state = e.Tick(now)
	return p
}

func petWindowOptions(view func(*ui.Context), cfg pet.Config, v pet.View) mygo.WindowOptions {
	return mygo.WindowOptions{
		Title: "爱弥斯桌宠", Content: ui.View(view), Hidden: true,
		Width: v.Size.X, Height: v.Size.Y, X: int(math.Round(v.Position.X)), Y: int(math.Round(v.Position.Y)),
		Frameless: true, Transparent: true, BackgroundColor: "#00000000", DisableShadow: true, Panel: petPlatformPanel(),
		DisableResize: true, DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true,
		AlwaysOnTop: cfg.AlwaysOnTop, SkipTaskbar: true,
	}
}

func petBubbleOptions(view func(*ui.Context)) mygo.WindowOptions {
	o := petWindowOptions(view, pet.DefaultConfig(), pet.View{Size: image.Pt(300, 164)})
	o.Title = "爱弥斯 · 对话气泡"
	return o
}

// run performs bounded UI dispatch; hiding the pet stops even animation dispatch,
// not just drawing. No native window or assistant is touched from this goroutine.
func (p *nativePet) run(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second / 30)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !p.active.Load() || !p.queued.CompareAndSwap(false, true) {
				continue
			}
			p.dispatch(func() {
				defer p.queued.Store(false)
				if ctx.Err() == nil && !p.closed && p.config.Visible {
					p.render(p.now())
				}
			})
		}
	}()
	return done
}

func (p *nativePet) updateScreens() {
	p.windowBoundsSet = false
	var areas []image.Rectangle
	for _, d := range p.screens() {
		r := d.WorkArea
		if r.Width > 0 && r.Height > 0 {
			areas = append(areas, image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height))
		}
	}
	p.workAreas = areas
	p.engine.SetScreens(areas)
	p.state = p.engine.Tick(p.now())
}

func petAssistant(s desktop.State) pet.AssistantSnapshot {
	return pet.AssistantSnapshot{Phase: s.Voice.Phase, Connected: s.Voice.Connected,
		Enabled: s.Settings.Voice.Enabled, Turn: s.Voice.Turn, Session: s.Voice.Session,
		Reply: s.Voice.Response, Error: s.Voice.Error}
}

func (p *nativePet) refreshAssistant(now time.Time) {
	if p == nil || p.closed || p.snapshot == nil {
		return
	}
	next := petAssistant(p.snapshot())
	if !p.assistantReady || next != p.assistant {
		p.assistantReady = true
		p.engine.SetAssistant(next, now)
		p.assistant = next
		p.render(now)
	}
}

func (p *nativePet) render(now time.Time) {
	if p.closed {
		return
	}
	before := p.state
	p.state = p.engine.Tick(now)
	if before.CanCancel != p.state.CanCancel {
		p.updateMenu()
	}
	if !p.config.Visible {
		return
	}
	if p.win != nil {
		b := mygo.Rectangle{X: int(math.Round(p.state.Position.X)), Y: int(math.Round(p.state.Position.Y)), Width: p.state.Size.X, Height: p.state.Size.Y}
		if !p.windowBoundsSet {
			p.windowBounds, p.windowBoundsSet = p.win.Bounds(), true
		}
		if p.windowBounds != b {
			previous := p.windowBounds
			p.windowBounds = b // Cache before native callbacks can re-enter rendering.
			if p.moveWindow != nil && previous.Width == b.Width && previous.Height == b.Height {
				p.moveWindow(b)
			} else {
				p.win.SetBounds(b)
			}
			p.markDirty()
		}
		if before.Rect != p.state.Rect {
			p.win.Invalidate()
		}
	}
	p.renderBubble(now)
	if p.dirty && !p.pointerDown && !p.dragging && !now.Before(p.saveAt) {
		p.persistConfig()
	}
}

func (p *nativePet) markDirty() {
	if !p.dirty {
		p.saveAt = p.now().Add(2 * time.Second)
	}
	p.dirty = true
}

func (p *nativePet) persistConfig() {
	p.config.X, p.config.Y, p.config.PositionSet = p.state.Position.X, p.state.Position.Y, true
	if p.persist && p.save != nil {
		if err := p.save(p.path, p.config); err != nil {
			log.Printf("pet: save preferences: %v", err)
			p.showNotice("桌宠设置保存失败；本次调整仍然有效。")
		}
	}
	p.dirty = false
}

func (p *nativePet) close() {
	p.active.Store(false)
	p.closed = true
	if p.offScreens != nil {
		p.offScreens()
	}
	p.persistConfig()
}

func (p *nativePet) changeConfig(change func(*pet.Config)) {
	p.config.X, p.config.Y, p.config.PositionSet = p.state.Position.X, p.state.Position.Y, true
	change(&p.config)
	p.config = pet.NormalizeConfig(p.config)
	// A deliberate preference change may replace a malformed preferences file;
	// merely starting/closing the application never silently overwrites one.
	p.persist = true
	p.engine.SetConfig(p.config)
	p.state = p.engine.Tick(p.now())
	p.persistConfig()
	p.applyVisibility()
	p.render(p.now())
}

func (p *nativePet) setVisible(on bool) {
	if !on {
		p.endDrag()
	}
	p.changeConfig(func(c *pet.Config) { c.Visible = on })
}

func (p *nativePet) applyVisibility() {
	p.active.Store(p.config.Visible)
	if p.win == nil {
		return
	}
	p.win.SetAlwaysOnTop(p.config.AlwaysOnTop)
	if p.bubble != nil {
		p.bubble.SetAlwaysOnTop(p.config.AlwaysOnTop)
	}
	if p.config.Visible {
		p.win.ShowInactive()
		p.win.Invalidate()
	} else {
		p.win.Hide()
		if p.bubble != nil {
			p.bubble.Hide()
		}
		p.bubbleShown = false
	}
	p.updateMenu()
}

func (p *nativePet) recover() {
	p.ignoreMouse = false
	if p.win != nil {
		p.win.SetIgnoreMouseEvents(false)
	}
	if p.bubble != nil {
		p.bubble.SetIgnoreMouseEvents(false)
	}
	p.setVisible(true)
	p.updateScreens()
	p.engine.Click(p.now())
	p.render(p.now())
}

func (p *nativePet) setPaused(on bool) {
	p.paused = on
	p.engine.SetPaused(on)
	p.render(p.now())
	p.updateMenu()
}

func (p *nativePet) setIgnoreMouse(on bool) {
	if on && !p.canIgnoreMouse {
		p.showNotice("托盘不可用，已禁用鼠标穿透，避免无法恢复交互。")
		return
	}
	p.endDrag()
	p.ignoreMouse = on
	if p.win != nil {
		p.win.SetIgnoreMouseEvents(on)
	}
	if p.bubble != nil {
		p.bubble.SetIgnoreMouseEvents(on)
	}
	p.engine.SetHovered(false)
	p.updateMenu()
}

func (p *nativePet) endDrag() {
	if p.pointerDown || p.dragging {
		p.engine.EndDrag(p.now())
	}
	p.pointerDown, p.dragging = false, false
	p.engine.SetHovered(false)
}

// handlePointer runs synchronously when input arrives, before a UI frame. Window
// movement must not wait for the 30Hz animation tick or the next view rebuild.
// Leave the event unconsumed so MyGo retains its click counts and native menu.
func (p *nativePet) handlePointer(ev ui.InputEvent) bool {
	if ev.Kind == ui.InputPointerDown && (ev.Button == 1 || ev.Mods&ui.Ctrl != 0) {
		p.contextEntries = nil
		return false
	}
	if p.closed || !p.config.Visible || p.ignoreMouse || p.cursor == nil || ev.Button != 0 {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		p.suppressClick, p.pointerDown = false, true
		p.pressCursor, p.pressPosition = p.cursor(), p.state.Position
		p.pressDirty, p.pressSaveAt = p.dirty, p.saveAt
		p.engine.SetHovered(true)
	case ui.InputPointerMove:
		if p.pointerDown {
			p.moveDrag()
		}
	case ui.InputPointerUp:
		if !p.pointerDown {
			return false
		}
		if !ev.Canceled {
			p.moveDrag()
		} // Include the final release position.
		wasDragging := p.dragging
		if ev.Canceled && wasDragging {
			p.engine.DragTo(p.pressPosition)
		}
		p.endDrag()
		if ev.Canceled && wasDragging {
			p.dirty = false
			p.render(p.now())
			p.dirty, p.saveAt = p.pressDirty, p.pressSaveAt
		} else if wasDragging {
			p.render(p.now())
			p.persistConfig()
		}
	}
	return false
}

func (p *nativePet) moveDrag() {
	at := p.cursor()
	dx, dy := at.X-p.pressCursor.X, at.Y-p.pressCursor.Y
	if !p.dragging && dx*dx+dy*dy >= 16 {
		p.dragging = true
		p.engine.BeginDrag(p.now())
	}
	if p.dragging {
		p.suppressClick = true
		// Absolute screen deltas avoid feeding window motion back into local input.
		p.engine.DragTo(pet.Point{X: p.pressPosition.X + float64(dx), Y: p.pressPosition.Y + float64(dy)})
		p.render(p.now())
	}
}

func (p *nativePet) view(c *ui.Context) {
	c.Root().Background(ui.Transparent)
	e := ui.Box(c.Key("pet-character")).Fill().Label("爱弥斯桌宠").Role(ui.RoleButton)
	e.HandleInput(func(ev ui.InputEvent) bool {
		// Native macOS input bypasses MyGo's equal-local-coordinate move filter.
		// Keep toolkit cancellation handling for focus loss / pointer cancellation.
		if p.nativePointer && !ev.Canceled {
			return false
		}
		return p.handlePointer(ev)
	})
	e.ContextMenu(p.contextMenu)
	p.engine.SetHovered(e.Hovered() || p.pointerDown)
	double, clicked := e.DoubleClicked(), e.Clicked()
	if double {
		// MyGo reports double on the second down and single again on its up.
		if !p.dragging && !p.suppressClick {
			p.engine.DoubleClick(p.now())
			p.render(p.now())
		}
		p.suppressClick = !clicked
	} else if clicked {
		if !p.suppressClick {
			p.engine.Click(p.now())
			p.render(p.now())
		}
		p.suppressClick = false
	}
	if e.PressCanceled() {
		p.suppressClick = false
	}
	frame := p.state.Rect
	e.Draw(func(painter *ui.Painter, r ui.Rect) {
		painter.ImageRegion(p.atlas, ui.Rect{X: float32(frame.Min.X), Y: float32(frame.Min.Y), W: float32(frame.Dx()), H: float32(frame.Dy())}, r, ui.ImageOptions{})
	})
}

func (p *nativePet) showNotice(text string) {
	p.notice, p.noticeUntil = text, p.now().Add(6*time.Second)
	if p.config.Visible {
		p.renderBubble(p.now())
	}
}

func (p *nativePet) renderBubble(now time.Time) {
	if p.bubble == nil {
		return
	}
	text := p.state.Bubble
	if now.Before(p.noticeUntil) {
		text = p.notice
	}
	// Dismiss only this exact text. New response increments remain visible.
	if text == p.dismissed {
		text = ""
	} else if text != "" {
		p.dismissed = ""
	}
	if text == "" || p.dragging || p.ignoreMouse || !p.config.Visible {
		if p.bubbleShown {
			p.bubble.Hide()
			p.bubbleShown = false
		}
		return
	}
	changed := text != p.bubbleText || p.bubbleCancel != p.state.CanCancel
	p.bubbleText, p.bubbleCancel = text, p.state.CanCancel
	b := petBubbleBounds(p.state.Position, p.state.Size, p.workAreas)
	if p.bubble.Bounds() != b {
		p.bubble.SetBounds(b)
	}
	if !p.bubbleShown {
		p.bubble.ShowInactive()
		p.bubbleShown = true
		changed = true
	}
	if changed {
		p.bubble.Invalidate()
	}
}

func petBubbleBounds(pos pet.Point, size image.Point, areas []image.Rectangle) mygo.Rectangle {
	const w, h, gap = 300, 164, 8
	x, y := int(math.Round(pos.X))+size.X/2-w/2, int(math.Round(pos.Y))-h-gap
	center := image.Pt(int(pos.X)+size.X/2, int(pos.Y)+size.Y/2)
	var best image.Rectangle
	bestDistance := math.Inf(1)
	for _, a := range areas {
		dx := math.Max(math.Max(float64(a.Min.X-center.X), 0), float64(center.X-a.Max.X))
		dy := math.Max(math.Max(float64(a.Min.Y-center.Y), 0), float64(center.Y-a.Max.Y))
		if d := dx*dx + dy*dy; d < bestDistance {
			best, bestDistance = a, d
		}
	}
	if !best.Empty() {
		if y < best.Min.Y {
			y = int(math.Round(pos.Y)) + size.Y + gap
		}
		x = max(best.Min.X, min(x, max(best.Min.X, best.Max.X-w)))
		y = max(best.Min.Y, min(y, max(best.Min.Y, best.Max.Y-h)))
	}
	return mygo.Rectangle{X: x, Y: y, Width: w, Height: h}
}

func (p *nativePet) dismissBubble() {
	p.dismissed = p.bubbleText
	if p.bubble != nil {
		p.bubble.Hide()
	}
	p.bubbleShown = false
}

func (p *nativePet) bubbleView(c *ui.Context) {
	c.Root().Background(ui.Transparent)
	t := c.Theme()
	ui.Column(c.Key("pet-bubble")).Fill().Padding(12).Gap(7).Radius(14).
		Background(t.Surface).Border(1, t.Accent.Alpha(.25)).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "爱弥斯").FontSize(12).FontWeight(600).TextColor(t.Accent).Grow(1)
			ui.Button(c, "×").Label("关闭气泡").Size(24, 22).OnClick(p.dismissBubble)
		})
		ui.Text(c, p.bubbleText).FontSize(13).FillWidth().Grow(1).MinHeight(0).Clip()
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Button(c, "完整对话").FontSize(12).OnClick(func() {
				if p.openConversation != nil {
					p.openConversation()
				}
			})
			if p.bubbleCancel {
				observed := p.assistant
				ui.Button(c, "取消当前请求").FontSize(12).OnClick(func() { p.cancelVoiceFor(observed.Session, observed.Turn) })
			}
		})
	})
}

func (p *nativePet) cancelVoice() { p.cancelVoiceFor(p.assistant.Session, p.assistant.Turn) }

func (p *nativePet) cancelVoiceFor(session uint64, turn uint32) {
	if p.command == nil || p.snapshot == nil {
		return
	}
	// A context menu may have been open while another turn started. Never let
	// a stale cancel button cancel a different connection/turn.
	s := petAssistant(p.snapshot())
	if !p.state.CanCancel || !s.Connected || s.Session != session || s.Turn != turn {
		p.showNotice("当前对话已变化，请重试。")
		return
	}
	go func() {
		err := p.command("cancel", "")
		p.dispatch(func() {
			if !p.closed {
				if err != nil {
					p.showNotice("取消失败，请在主界面查看连接状态。")
				} else {
					p.showNotice("已发送取消请求。")
				}
			}
		})
	}()
}
