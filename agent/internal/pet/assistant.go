package pet

import (
	"strings"
	"time"
	"unicode"
)

// AssistantSnapshot is an owned, dependency-free projection of desktop.State:
// Phase/Connected/Turn/Session/Reply/Error come from Voice (Reply=Response),
// Enabled comes from Settings.Voice.Enabled. Do not copy API keys or memory here.
type AssistantSnapshot struct {
	Phase     string
	Connected bool
	Enabled   bool
	Turn      uint32
	Session   uint64
	Reply     string
	Error     string
}

const (
	BubbleTTL       = 6 * time.Second
	BubbleRuneLimit = 80
)

type AssistantView struct {
	Action    Action
	Bubble    string
	CanCancel bool
	Active    bool
}

// Projector consumes snapshots only. It never sends commands, replays audio,
// performs I/O, or starts a second assistant. The UI owns its one cancel callback.
// Call Update/View on the UI goroutine, or serialize externally.
type Projector struct {
	initialized  bool
	snapshot     AssistantSnapshot
	bubble       string
	bubbleUntil  time.Time
	replyStarted bool
	replyUntil   time.Time
	lastNow      time.Time
	failureUntil time.Time
}

// ShortBubble truncates by Unicode code points (not UTF-8 bytes), folds whitespace,
// and includes the ellipsis in the limit. Combining sequences may occupy >1 rune.
func ShortBubble(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	text = strings.Join(strings.FieldsFunc(strings.ToValidUTF8(text, "�"), unicode.IsSpace), " ")
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	return string(r[:limit-1]) + "…"
}

func assistantPhase(s AssistantSnapshot) AssistantView {
	if !s.Enabled {
		return AssistantView{Action: Idle}
	}
	if s.Phase == "error" {
		return AssistantView{Action: Failed}
	}
	if !s.Connected {
		return AssistantView{Action: Idle}
	}
	v := AssistantView{CanCancel: true, Active: true}
	switch s.Phase {
	case "wake", "listening":
		v.Action = Wait
	case "recognizing", "thinking":
		v.Action = Review
	case "synthesizing", "speaking":
		v.Action = Running
	default:
		return AssistantView{Action: Idle}
	}
	return v
}
func phaseBubble(s AssistantSnapshot) string {
	if !s.Enabled {
		return ""
	}
	if s.Phase == "error" {
		return "遇到问题，请在助手中重试"
	}
	if !s.Connected {
		return ""
	}
	switch s.Phase {
	case "wake", "listening":
		return "我在听…"
	case "recognizing":
		return "正在辨认…"
	case "thinking":
		return "让我想想…"
	case "synthesizing":
		return "准备回答…"
	case "speaking":
		return "正在回答…"
	}
	return ""
}
func (p *Projector) clock(now time.Time) time.Time {
	if now.Before(p.lastNow) {
		return p.lastNow
	}
	p.lastNow = now
	return now
}

func (p *Projector) Update(s AssistantSnapshot, now time.Time) {
	now = p.clock(now)
	// Enabled is a global preference, not a turn event. The host deliberately
	// emits Session=0 when disabled, so apply it even after a later session.
	if p.initialized && !s.Enabled {
		p.snapshot.Enabled = false
		p.snapshot.Connected = false
		p.snapshot.Phase = "disabled"
		p.bubble = ""
		p.bubbleUntil = time.Time{}
		return
	}
	if p.initialized && (s.Session < p.snapshot.Session || (s.Session == p.snapshot.Session && s.Turn < p.snapshot.Turn)) {
		return
	}
	previous := p.snapshot
	baseline := !p.initialized || s.Session != previous.Session
	newTurn := baseline || s.Turn != previous.Turn
	if newTurn {
		p.bubble = ""
		p.bubbleUntil = time.Time{}
		p.replyStarted = false
		p.replyUntil = time.Time{}
	}
	// Repeated 30Hz snapshots must not refresh any bubble's deadline.
	phaseChanged := baseline || newTurn || s.Phase != previous.Phase || s.Connected != previous.Connected || s.Enabled != previous.Enabled
	if phaseChanged && !p.replyStarted {
		p.bubble = phaseBubble(s)
		p.bubbleUntil = now.Add(BubbleTTL)
	}
	if !s.Enabled || (!s.Connected && s.Phase != "error") {
		p.bubble = ""
		p.bubbleUntil = time.Time{}
	} else if s.Connected && !baseline && s.Turn > 0 && s.Reply != "" && s.Reply != previous.Reply && s.Phase != "error" {
		// The first snapshot of an existing session is only a baseline: never replay
		// retained Response. Subsequent changes in the current turn may be streamed.
		if !p.replyStarted {
			p.replyStarted = true
			p.replyUntil = now.Add(BubbleTTL)
		}
		if now.Before(p.replyUntil) {
			p.bubble = ShortBubble(s.Reply, BubbleRuneLimit)
			p.bubbleUntil = p.replyUntil
		}
	}
	if s.Phase == "error" && phaseChanged {
		// A failure is a one-shot reaction, not a permanent interaction lock.
		p.failureUntil = now.Add(time.Duration(frameCount(Failed)) * ActionFrameDuration(Failed))
		p.bubble = phaseBubble(s)
		p.bubbleUntil = now.Add(BubbleTTL)
	}
	p.snapshot = s
	p.initialized = true
}
func (p *Projector) View(now time.Time) AssistantView {
	now = p.clock(now)
	v := assistantPhase(p.snapshot)
	if v.Action == Failed && !now.Before(p.failureUntil) {
		v.Action = Idle
	}
	if now.Before(p.bubbleUntil) {
		v.Bubble = p.bubble
	}
	return v
}
