package pet

import (
	"image"
	"math"
	"time"
)

type Point struct{ X, Y float64 }

type View struct {
	Action    Action
	Frame     int
	Rect      image.Rectangle
	Position  Point
	Size      image.Point
	Bubble    string
	CanCancel bool
	Dragging  bool
}

// Engine is a single-owner pure state machine. Every time is supplied by the
// caller; Tick can run at 30Hz while sprite frames retain their original 140/150ms.
// Priority is drag > assistant/chat > click > autonomous idle/wandering.
// Pausing/hovering only disables autonomous actions, not assistant or clicks.
type Engine struct {
	cfg                            Config
	screens                        []image.Rectangle
	position                       Point
	positioned                     bool
	dragging, paused, hovered      bool
	projector                      Projector
	action                         Action
	actionSince, lastTick, lastNow time.Time
	clickAction                    Action
	clickUntil                     time.Time
	autoAction                     Action
	autoUntil, nextAuto            time.Time
	rng                            uint64
}

func NewEngine(cfg Config, now time.Time) *Engine {
	cfg = NormalizeConfig(cfg)
	return &Engine{cfg: cfg, position: Point{cfg.X, cfg.Y}, positioned: cfg.PositionSet, action: Idle, actionSince: now, lastTick: now, lastNow: now, nextAuto: now.Add(activityInterval(cfg.Frequency)), rng: 1}
}
func (e *Engine) clock(now time.Time) time.Time {
	if now.Before(e.lastNow) {
		return e.lastNow
	}
	e.lastNow = now
	return now
}
func (e *Engine) size() image.Point {
	return image.Pt(int(math.Round(FrameWidth*e.cfg.Scale)), int(math.Round(FrameHeight*e.cfg.Scale)))
}
func (e *Engine) SetScreens(screens []image.Rectangle) {
	e.screens = nil
	for _, r := range screens {
		if !r.Empty() {
			e.screens = append(e.screens, r)
		}
	}
	if !e.positioned && len(e.screens) > 0 {
		r := e.screens[0]
		size := e.size()
		e.position = Point{float64(r.Max.X - size.X - 24), float64(r.Max.Y - size.Y - 24)}
		e.positioned = true
	}
	e.position = ClampPosition(e.position, e.size(), e.screens)
}
func (e *Engine) SetConfig(cfg Config) {
	e.cfg = NormalizeConfig(cfg)
	if e.cfg.PositionSet {
		e.position = Point{e.cfg.X, e.cfg.Y}
		e.positioned = true
	}
	e.position = ClampPosition(e.position, e.size(), e.screens)
	e.stopAuto()
}

// Config returns current geometry so the UI can persist after drag or shutdown.
func (e *Engine) Config() Config {
	c := e.cfg
	c.X = e.position.X
	c.Y = e.position.Y
	c.PositionSet = e.positioned
	return c
}
func (e *Engine) SetAssistant(s AssistantSnapshot, now time.Time) {
	now = e.clock(now)
	e.projector.Update(s, now)
	v := e.projector.View(now)
	if v.Active || v.Action == Failed {
		e.clickUntil = time.Time{}
		e.stopAuto()
	}
}
func (e *Engine) Click(now time.Time)       { e.click(Wave, now) }
func (e *Engine) DoubleClick(now time.Time) { e.click(Jump, now) }
func (e *Engine) click(action Action, now time.Time) {
	now = e.clock(now)
	// Events received while higher-priority work is active are discarded, not
	// queued to surprise the user after chat/drag completes.
	assistant := e.projector.View(now)
	if e.dragging || assistant.Active || assistant.Action == Failed {
		return
	}
	e.clickAction = action
	e.action = action
	e.actionSince = now
	e.clickUntil = now.Add(time.Duration(frameCount(action)) * ActionFrameDuration(action))
	e.stopAuto()
}
func (e *Engine) BeginDrag(now time.Time) {
	e.clock(now)
	e.dragging = true
	e.clickUntil = time.Time{}
	e.stopAuto()
}

// DragTo takes the absolute pet-window top-left, not the pointer delta.
func (e *Engine) DragTo(position Point) {
	if e.dragging {
		e.position = ClampPosition(position, e.size(), e.screens)
		e.positioned = true
	}
}
func (e *Engine) EndDrag(now time.Time) {
	now = e.clock(now)
	e.dragging = false
	e.nextAuto = now.Add(activityInterval(e.cfg.Frequency))
}
func (e *Engine) SetPaused(paused bool) {
	e.paused = paused
	if paused {
		e.stopAuto()
	}
}
func (e *Engine) SetHovered(hovered bool) {
	e.hovered = hovered
	if hovered {
		e.stopAuto()
	}
}
func (e *Engine) stopAuto() {
	e.autoAction = Idle
	e.autoUntil = time.Time{}
	e.nextAuto = e.lastNow.Add(activityInterval(e.cfg.Frequency))
}
func activityInterval(f Frequency) time.Duration {
	switch f {
	case Slow:
		return 18 * time.Second
	case Fast:
		return 5 * time.Second
	default:
		return 10 * time.Second
	}
}
func (e *Engine) random() uint64 {
	e.rng = e.rng*6364136223846793005 + 1442695040888963407
	return e.rng >> 32
}

func (e *Engine) Tick(now time.Time) View {
	now = e.clock(now)
	dt := now.Sub(e.lastTick)
	if dt < 0 {
		dt = 0
	}
	if dt > 250*time.Millisecond {
		dt = 250 * time.Millisecond
	}
	e.lastTick = now
	assistant := e.projector.View(now)
	action := Idle
	switch {
	case e.dragging:
		action = Idle
	case assistant.Active || assistant.Action == Failed:
		action = assistant.Action
		e.clickUntil = time.Time{}
		e.stopAuto()
	case now.Before(e.clickUntil):
		action = e.clickAction
	case !e.paused && !e.hovered && e.cfg.Visible:
		if !now.Before(e.autoUntil) {
			e.autoAction = Idle
		}
		if !now.Before(e.nextAuto) {
			options := []Action{Idle, Wave, Jump}
			if e.cfg.Wander {
				options = append(options, RunLeft, RunRight)
			}
			e.autoAction = options[e.random()%uint64(len(options))]
			duration := time.Duration(frameCount(e.autoAction)) * ActionFrameDuration(e.autoAction)
			if e.autoAction == RunLeft || e.autoAction == RunRight {
				duration = 3 * time.Second
			}
			e.autoUntil = now.Add(duration)
			e.nextAuto = now.Add(duration + activityInterval(e.cfg.Frequency))
		}
		action = e.autoAction
	}
	if !e.cfg.Wander && (action == RunLeft || action == RunRight) {
		action = Idle
	}
	if action == RunLeft || action == RunRight {
		direction := 1.0
		if action == RunLeft {
			direction = -1
		}
		old := e.position
		e.position.X += direction * 64 * e.cfg.Scale * dt.Seconds()
		e.position = ClampPosition(e.position, e.size(), e.screens)
		e.positioned = true
		if e.position == old {
			action = Idle
			e.stopAuto()
		}
	}
	if action != e.action {
		e.action = action
		e.actionSince = now
	}
	elapsed := now.Sub(e.actionSince)
	if elapsed < 0 {
		elapsed = 0
	}
	frame := int(elapsed/ActionFrameDuration(action)) % frameCount(action)
	v := View{Action: action, Frame: frame, Rect: Frame(action, frame), Position: e.position, Size: e.size(), CanCancel: assistant.CanCancel, Dragging: e.dragging}
	if !e.dragging {
		v.Bubble = assistant.Bubble
	}
	return v
}

// ClampPosition selects the nearest individual work area, not the bounding union
// of displays (which can contain inaccessible gaps). Negative monitor origins
// and removed monitors are supported. An oversized pet is anchored to Min.
// With no known work areas, finite coordinates are retained.
func ClampPosition(p Point, size image.Point, screens []image.Rectangle) Point {
	if !finite(p.X) {
		p.X = 0
	}
	if !finite(p.Y) {
		p.Y = 0
	}
	best := p
	distance := math.Inf(1)
	for _, r := range screens {
		if r.Empty() {
			continue
		}
		maxX := float64(max(r.Min.X, r.Max.X-size.X))
		maxY := float64(max(r.Min.Y, r.Max.Y-size.Y))
		candidate := Point{math.Max(float64(r.Min.X), math.Min(maxX, p.X)), math.Max(float64(r.Min.Y), math.Min(maxY, p.Y))}
		dx, dy := candidate.X-p.X, candidate.Y-p.Y
		d := dx*dx + dy*dy
		if d < distance {
			best = candidate
			distance = d
		}
	}
	return best
}
