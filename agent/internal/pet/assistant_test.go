package pet

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var testNow = time.Unix(1700000000, 0)

func snapshot(phase string, turn uint32) AssistantSnapshot {
	return AssistantSnapshot{Phase: phase, Connected: true, Enabled: true, Turn: turn, Session: 1}
}
func TestAssistantPhaseMappingAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		phase  string
		action Action
		active bool
	}{{"idle", Idle, false}, {"wake", Wait, true}, {"listening", Wait, true}, {"recognizing", Review, true}, {"thinking", Review, true}, {"synthesizing", Running, true}, {"speaking", Running, true}, {"error", Failed, false}, {"connecting", Idle, false}, {"disabled", Idle, false}} {
		t.Run(tc.phase, func(t *testing.T) {
			var p Projector
			s := snapshot(tc.phase, 1)
			p.Update(s, testNow)
			v := p.View(testNow)
			if v.Action != tc.action || v.Active != tc.active || v.CanCancel != tc.active {
				t.Fatalf("%+v", v)
			}
			if tc.active {
				s.Connected = false
				p.Update(s, testNow)
				v = p.View(testNow)
				if v.Active || v.CanCancel || v.Action != Idle || v.Bubble != "" {
					t.Fatal(v)
				}
			}
			s.Enabled = false
			p.Update(s, testNow)
			v = p.View(testNow)
			if v.Action != Idle || v.Bubble != "" || v.CanCancel {
				t.Fatal(v)
			}
		})
	}
	// Voice Error can contain a nonfatal wake-model diagnostic during listening.
	s := snapshot("listening", 1)
	s.Error = "manual fixed recording diagnostic"
	var p Projector
	p.Update(s, testNow)
	if p.View(testNow).Action != Wait {
		t.Fatal("nonfatal diagnostic became Failed")
	}
}
func TestAssistantNoStartupOrReconnectReplyReplay(t *testing.T) {
	var p Projector
	s := snapshot("idle", 9)
	s.Reply = "old retained response"
	p.Update(s, testNow)
	for i := 0; i < 10; i++ {
		p.Update(s, testNow.Add(time.Duration(i)*time.Second))
		if p.View(testNow.Add(time.Duration(i)*time.Second)).Bubble != "" {
			t.Fatal("old reply replayed")
		}
	}
	s.Session = 2
	s.Turn = 1
	s.Reply = "old on reconnect"
	p.Update(s, testNow.Add(11*time.Second))
	if p.View(testNow.Add(11*time.Second)).Bubble != "" {
		t.Fatal("reconnect replayed")
	}
	s = snapshot("thinking", 1)
	s.Session = 2
	s.Reply = ""
	p.Update(s, testNow.Add(12*time.Second))
	s.Reply = "new streamed response"
	p.Update(s, testNow.Add(13*time.Second))
	if p.View(testNow.Add(13*time.Second)).Bubble != s.Reply {
		t.Fatal("new response lost")
	}
}
func TestAssistantReplyOnlyCurrentTurnUnicodeAndDeadline(t *testing.T) {
	var p Projector
	p.Update(snapshot("idle", 0), testNow)
	s := snapshot("thinking", 1)
	p.Update(s, testNow.Add(time.Second))
	s.Reply = strings.Repeat("你好🐈", 50)
	p.Update(s, testNow.Add(2*time.Second))
	want := ShortBubble(s.Reply, BubbleRuneLimit)
	v := p.View(testNow.Add(2 * time.Second))
	if v.Bubble != want || !utf8.ValidString(v.Bubble) || utf8.RuneCountInString(v.Bubble) != BubbleRuneLimit {
		t.Fatal(v)
	}
	// Stream updates and repeated snapshots do not extend the per-turn display cap.
	for i := 3; i <= 9; i++ {
		s.Reply += "！"
		p.Update(s, testNow.Add(time.Duration(i)*time.Second))
	}
	if v = p.View(testNow.Add(9 * time.Second)); v.Bubble != "" {
		t.Fatal("unbounded streaming bubble", v)
	}
	s.Turn = 2
	s.Reply = ""
	s.Phase = "listening"
	p.Update(s, testNow.Add(10*time.Second))
	if v = p.View(testNow.Add(10 * time.Second)); v.Bubble != "我在听…" {
		t.Fatal(v)
	}
	stale := snapshot("speaking", 1)
	stale.Reply = "previous turn"
	p.Update(stale, testNow.Add(11*time.Second))
	if v = p.View(testNow.Add(11 * time.Second)); v.Action != Wait || v.Bubble == stale.Reply {
		t.Fatal("stale turn accepted", v)
	}
	s.Reply = "当前轮"
	s.Phase = "speaking"
	p.Update(s, testNow.Add(12*time.Second))
	if p.View(testNow.Add(12*time.Second)).Bubble != "当前轮" {
		t.Fatal("current turn missing")
	}
	s.Enabled = false
	p.Update(s, testNow.Add(13*time.Second))
	if p.View(testNow.Add(13*time.Second)).Bubble != "" {
		t.Fatal("disabled bubble")
	}
}
func TestAssistantStatusExpiresWithoutRefreshAndErrorsAreGeneric(t *testing.T) {
	var p Projector
	s := snapshot("thinking", 1)
	p.Update(s, testNow)
	for i := 0; i < 200; i++ {
		p.Update(s, testNow.Add(time.Duration(i)*time.Second/30))
	}
	if p.View(testNow.Add(7*time.Second)).Bubble != "" {
		t.Fatal("status refreshed forever")
	}
	s.Phase = "error"
	s.Error = "https://secret/?api_key=never-display"
	p.Update(s, testNow.Add(8*time.Second))
	v := p.View(testNow.Add(8 * time.Second))
	if v.Action != Failed || v.Bubble == "" || strings.Contains(v.Bubble, "secret") {
		t.Fatal(v)
	}
	if p.View(testNow.Add(14*time.Second)).Bubble != "" {
		t.Fatal("error bubble never expired")
	}
	// A delayed snapshot from a superseded session cannot replace current output.
	s.Session = 2
	s.Turn = 1
	s.Phase = "listening"
	p.Update(s, testNow.Add(15*time.Second))
	stale := snapshot("error", 100)
	p.Update(stale, testNow.Add(16*time.Second))
	if p.View(testNow.Add(16*time.Second)).Action != Wait {
		t.Fatal("stale session accepted")
	}
}
func TestShortBubbleUnicode(t *testing.T) {
	for _, tc := range []struct {
		text  string
		limit int
		want  string
	}{{"你好🐈世界", 4, "你好🐈…"}, {"abc", 1, "…"}, {"a", 1, "a"}, {"abc", 0, ""}, {"a\n\t b", 5, "a b"}, {"", 3, ""}, {"ééé", 2, "é…"}} {
		if got := ShortBubble(tc.text, tc.limit); got != tc.want {
			t.Fatalf("%q != %q", got, tc.want)
		}
	}
	if !utf8.ValidString(ShortBubble(string([]byte{255, 254, 'a'}), 5)) {
		t.Fatal("invalid UTF-8 preserved")
	}
}
