package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func fishStreamTestConfig() Config {
	return Config{Enabled: true, TTSProvider: "fish", FishAPIKey: "fake-fish", FishReferenceID: "fake-reference", Volume: 70}
}

// Read every message rather than discarding control frames: an unexpected EOF,
// early short frame, or extra playback_start must fail wire-accounting tests.
func streamReadControl(d testDevice, want string) (message, map[string]json.RawMessage, error) {
	for {
		typ, b, e := d.read()
		if e != nil {
			return message{}, nil, e
		}
		if typ != websocket.MessageText {
			return message{}, nil, fmt.Errorf("unexpected PCM before %s", want)
		}
		m, e := decodeMessage(b)
		if e != nil {
			return m, nil, e
		}
		if m.Type == want {
			var raw map[string]json.RawMessage
			e = json.Unmarshal(b, &raw)
			return m, raw, e
		}
		if m.Type == "error" || m.Type == "playback_end" || m.Type == "playback_start" {
			return m, nil, fmt.Errorf("unexpected %s before %s: %s", m.Type, want, b)
		}
	}
}
func streamReadFrame(d testDevice, turn, seq uint32, n int, value int16) error {
	for {
		typ, b, e := d.read()
		if e != nil {
			return e
		}
		if typ == websocket.MessageText {
			m, e := decodeMessage(b)
			if e != nil {
				return e
			}
			if m.Type == "error" || m.Type == "playback_end" || m.Type == "playback_start" {
				return fmt.Errorf("unexpected control before PCM: %s", b)
			}
			continue
		}
		f, e := decodeAudio(b)
		if e != nil {
			return e
		}
		if f.Kind != kindPlayback || f.Epoch != d.epoch || f.Turn != turn || f.Seq != seq || len(f.PCM) != n {
			return fmt.Errorf("bad frame epoch=%d turn=%d seq=%d len=%d", f.Epoch, f.Turn, f.Seq, len(f.PCM))
		}
		for _, v := range f.PCM {
			if v != value {
				return fmt.Errorf("unexpected old or reordered PCM %d, want %d", v, value)
			}
		}
		return nil
	}
}

func TestFishStreamSessionUnknownTotalFramesAndActualACK(t *testing.T) {
	dep := fakeDependencies()
	produce := make(chan int)
	produced := make(chan struct{}, 8)
	fishDone := make(chan struct{})
	speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
		defer close(fishDone)
		select {
		case <-in:
		case <-c.Done():
			return c.Err()
		}
		for {
			select {
			case n := <-produce:
				if n == 0 {
					return nil
				}
				p := make([]int16, n)
				for i := range p {
					p[i] = 7
				}
				if e := audio(p); e != nil {
					return e
				}
				produced <- struct{}{}
			case <-c.Done():
				return c.Err()
			}
		}
	}}
	dep.newSpeech = func(Config) speechAPI { return speech }
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) {
		return nil, errors.New("preview initialized LLM")
	}
	ready := make(chan struct{})
	finished := make(chan struct{})
	beforeACK := make(chan struct{})
	allowACK := make(chan struct{})
	var commands chan Command
	commands, _, snapshots := sessionHarness(t, fishStreamTestConfig(), dep, func(d testDevice) error {
		defer close(finished)
		<-ready
		commands <- Command{Kind: "preview", Text: "测试流式播放"}
		produce <- 80
		<-produced
		start, raw, e := streamReadControl(d, "playback_start")
		if e != nil {
			return e
		}
		if !start.Streaming || start.Samples != 0 || string(raw["samples"]) != "0" || string(raw["streaming"]) != "true" {
			return fmt.Errorf("unknown-total fields not explicit: %#v", raw)
		}
		if e = d.send(message{Type: "credit", Turn: start.Turn, Frames: 4}); e != nil {
			return e
		}
		// The first 80 samples cannot be sent as a short non-final frame.
		produce <- 240
		<-produced
		if e = streamReadFrame(d, start.Turn, 0, 320, 7); e != nil {
			return e
		}
		produce <- 80
		<-produced
		produce <- 240
		<-produced
		if e = streamReadFrame(d, start.Turn, 1, 320, 7); e != nil {
			return e
		}
		if e = d.send(message{Type: "playing", Turn: start.Turn}); e != nil {
			return e
		}
		if _, _, e = streamReadControl(d, "state"); e != nil {
			return e
		}
		// A temporary empty host queue is still an open stream. Only backend EOF
		// permits the final 1-sample frame and playback_end.
		produce <- 1
		<-produced
		produce <- 0
		if e = streamReadFrame(d, start.Turn, 2, 1, 7); e != nil {
			return e
		}
		end, raw, e := streamReadControl(d, "playback_end")
		if e != nil {
			return e
		}
		if end.Turn != start.Turn || end.Samples != 641 || string(raw["samples"]) != "641" {
			return fmt.Errorf("wrong final accounting: %+v %#v", end, raw)
		}
		close(beforeACK)
		<-allowACK
		if e = d.send(message{Type: "played", Turn: start.Turn}); e != nil {
			return e
		}
		return d.untilPhase("idle")
	})
	close(ready)
	pipelineTestWait(t, beforeACK, "stream end before actual played ACK")
	states := snapshots()
	last := states[len(states)-1]
	if last.Phase != "speaking" || last.PlayedSegments != 0 || last.FirstAudioMillis <= 0 {
		t.Fatalf("turn completed before actual played: %+v", last)
	}
	close(allowACK)
	pipelineTestWait(t, finished, "actual played ACK")
	if speech.calls.Load() != 1 || speech.synthCalls.Load() != 0 {
		t.Fatal("preview used fallback or repeated Fish")
	}
	pipelineTestClosed(t, fishDone, "Fish completion")
}

func TestFishStreamSessionBackpressureCancellationAndStaleIsolation(t *testing.T) {
	for _, action := range []string{"cancel", "mute", "clear", "wake"} {
		t.Run(action, func(t *testing.T) {
			dep := fakeDependencies()
			attempt117 := make(chan struct{})
			passed117 := make(chan struct{})
			streamJoined := make(chan struct{})
			var completed atomic.Int32
			speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
				defer close(streamJoined)
				select {
				case <-in:
				case <-c.Done():
					return c.Err()
				}
				p := make([]int16, 320)
				for i := range p {
					p[i] = 9
				}
				for i := 1; i <= 1000; i++ {
					if i == 117 {
						close(attempt117)
					}
					if e := audio(p); e != nil {
						return e
					}
					completed.Add(1)
					if i == 117 {
						close(passed117)
					}
				}
				return errors.New("unbounded streaming producer drained")
			}}
			dep.newSpeech = func(Config) speechAPI { return speech }
			ready := make(chan struct{})
			finished := make(chan struct{})
			var commands chan Command
			commands, _, _ = sessionHarness(t, fishStreamTestConfig(), dep, func(d testDevice) error {
				defer close(finished)
				<-ready
				commands <- Command{Kind: "preview"}
				start, _, e := streamReadControl(d, "playback_start")
				if e != nil {
					return e
				}
				// No credits: 100 host frames (2s) +16 result slots. Callback117 must block.
				select {
				case <-attempt117:
				case <-d.ctx.Done():
					return d.ctx.Err()
				}
				select {
				case <-passed117:
					return errors.New("host/result backpressure bound exceeded")
				case <-time.After(25 * time.Millisecond):
				}
				if n := completed.Load(); n != 116 {
					return fmt.Errorf("wrong withheld-credit bound %d, want116", n)
				}
				if action == "mute" {
					e = d.send(message{Type: "mute", Muted: true})
				} else {
					commands <- Command{Kind: action}
				}
				if e != nil {
					return e
				}
				phase := "idle"
				if action == "wake" {
					phase = "listening"
				}
				if e = d.untilPhase(phase); e != nil {
					return e
				}
				select {
				case <-streamJoined:
				case <-d.ctx.Done():
					return errors.New("cancellation did not join Fish")
				}
				commands <- Command{Kind: "reference"}
				next, _, e := streamReadControl(d, "playback_start")
				if e != nil {
					return e
				}
				if next.Turn <= start.Turn || next.Streaming {
					return errors.New("new fixed reference did not replace old stream")
				}
				// Old controls and queued PCM must not modify the new reference playback.
				for _, m := range []message{{Type: "credit", Turn: start.Turn, Frames: 32}, {Type: "playing", Turn: start.Turn}, {Type: "played", Turn: start.Turn}, {Type: "credit", Epoch: 43, Turn: next.Turn, Frames: 32}} {
					if e = d.send(m); e != nil {
						return e
					}
				}
				if e = d.send(message{Type: "credit", Turn: next.Turn, Frames: 32}); e != nil {
					return e
				}
				for seq, n := range []int{320, 320, 1} {
					if e = streamReadFrame(d, next.Turn, uint32(seq), n, 0); e != nil {
						return e
					}
				}
				if _, _, e = streamReadControl(d, "playback_end"); e != nil {
					return e
				}
				if e = d.send(message{Type: "playing", Turn: next.Turn}); e != nil {
					return e
				}
				if e = d.send(message{Type: "played", Turn: next.Turn}); e != nil {
					return e
				}
				return d.untilPhase("idle")
			})
			close(ready)
			pipelineTestWait(t, finished, "bounded cancellation "+action)
		})
	}
}

func TestFishStreamSessionRejectsMissingCapabilityBeforeCloud(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var calls atomic.Int32
	dep := fakeDependencies()
	dep.newSpeech = func(Config) speechAPI { calls.Add(1); return &fakeSpeech{} }
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { calls.Add(1); return &fakeBrain{}, nil }
	dep.newDetector = func(Config) (detector, error) { calls.Add(1); return &fakeDetector{}, nil }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer ws.CloseNow()
		d := testDevice{ws: ws, ctx: ctx, epoch: 42}
		d.send(message{Type: "hello", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le", FrameSamples: 320, Capabilities: []string{"capture", "playback"}})
		ws.Read(ctx)
	}))
	defer server.Close()
	commands := make(chan Command, 1)
	commands <- Command{Kind: "preview"}
	var last State
	e := runWithDependencies(ctx, strings.TrimPrefix(server.URL, "http://"), fishStreamTestConfig(), nil, commands, func(s State) { last = s }, dep)
	if e == nil || !strings.Contains(e.Error(), "固件") || calls.Load() != 0 || last.Connected {
		t.Fatalf("missing capability not rejected before initialization: err=%v calls=%d state=%+v", e, calls.Load(), last)
	}
}

func TestFishStreamSessionPlayingAfterBufferCompaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		accepted <- ws
		<-ctx.Done()
		ws.CloseNow()
	}))
	defer server.Close()
	host, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer host.CloseNow()
	device := <-accepted
	defer device.CloseNow()
	s := &session{ctx: ctx, ws: host, cfg: fishStreamTestConfig(), state: State{Turn: 1}, epoch: 42, active: true, wireTurn: 1, firstWireTurn: 1, emit: func(State) {}, cloudStarted: time.Now()}
	if e = s.startAudioStream(); e != nil {
		t.Fatal(e)
	}
	if e = s.appendAudioStream(make([]int16, 320)); e != nil {
		t.Fatal(e)
	}
	s.play.credits = 1
	if e = s.pump(); e != nil {
		t.Fatal(e)
	}
	if e = s.appendAudioStream(make([]int16, 80)); e != nil {
		t.Fatal(e)
	}
	if s.play.seq != 1 || s.play.offset != 0 {
		t.Fatal("test did not establish sent-and-compacted state")
	}
	b, _ := json.Marshal(message{V: 1, Type: "playing", Epoch: 42, Turn: 1})
	if e = s.control(b); e != nil {
		t.Fatal(e)
	}
	if !s.play.playing || s.state.Phase != "speaking" {
		t.Fatal("actual playing ACK ignored after compaction")
	}
	// Keep the temporary 80-sample tail until input EOF, then permit final drain.
	if s.play.ended {
		t.Fatal("temporary stream empty marked ended")
	}
	s.play.credits = 1
	if e = s.finishAudioStream(400); e != nil {
		t.Fatal(e)
	}
	b, _ = json.Marshal(message{V: 1, Type: "played", Epoch: 42, Turn: 1})
	if e = s.control(b); e != nil {
		t.Fatal(e)
	}
	if s.active || s.state.PlayedSegments != 1 {
		t.Fatal("actual played did not finish compacted stream")
	}
}
