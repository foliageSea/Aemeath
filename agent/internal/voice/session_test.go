package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type fakeDetector struct {
	samples, end int64
	hitAt        int64
	closed       atomic.Bool
}

func (d *fakeDetector) Feed(p []int16) (bool, error) {
	d.samples += int64(len(p))
	return d.hitAt > 0 && d.samples == d.hitAt, nil
}
func (d *fakeDetector) KeywordEndSamples() int64 { return d.end }
func (d *fakeDetector) Reset()                   { d.samples = 0 }
func (d *fakeDetector) Close()                   { d.closed.Store(true) }

type fakeVAD struct{ closed atomic.Bool }

func (v *fakeVAD) Speech(p []int16) (bool, error) { return len(p) > 0 && p[0] > 0, nil }
func (v *fakeVAD) Reset()                         {}
func (v *fakeVAD) Close()                         { v.closed.Store(true) }

type fakeSpeech struct {
	recognize  func(context.Context, []int16) (string, error)
	synthesize func(context.Context, string, string) ([]int16, error)
}

func (f *fakeSpeech) Recognize(c context.Context, p []int16) (string, error) {
	if f.recognize != nil {
		return f.recognize(c, p)
	}
	return "几点了", nil
}
func (f *fakeSpeech) Synthesize(c context.Context, s, r string) ([]int16, error) {
	if f.synthesize != nil {
		return f.synthesize(c, s, r)
	}
	return make([]int16, 641), nil
}

type fakeBrain struct{ clears atomic.Int32 }

func (f *fakeBrain) Reply(context.Context, string) (string, error) { return "现在八点。", nil }
func (f *fakeBrain) Clear()                                        { f.clears.Add(1) }
func fakeDependencies() dependencies {
	return dependencies{
		newDetector: func(Config) (detector, error) { return &fakeDetector{}, nil },
		newVAD:      func(Config) (vad, error) { return &fakeVAD{}, nil },
		newSpeech:   func(Config) speechAPI { return &fakeSpeech{} },
		newBrain:    func(context.Context, Config, Host) (brainAPI, error) { return &fakeBrain{}, nil },
		reference:   func(string) ([]int16, error) { return make([]int16, 641), nil },
	}
}

type testDevice struct {
	ws    *websocket.Conn
	ctx   context.Context
	epoch uint32
}

func (d testDevice) send(m message) error {
	m.V = 1
	if m.Epoch == 0 {
		m.Epoch = d.epoch
	}
	b, _ := json.Marshal(m)
	return d.ws.Write(d.ctx, websocket.MessageText, b)
}
func (d testDevice) read() (websocket.MessageType, []byte, error) { return d.ws.Read(d.ctx) }
func (d testDevice) until(kind string) (message, error) {
	for {
		typ, b, e := d.read()
		if e != nil {
			return message{}, e
		}
		if typ == websocket.MessageText {
			m, e := decodeMessage(b)
			if e != nil {
				return m, e
			}
			if m.Type == kind {
				return m, nil
			}
		}
	}
}
func (d testDevice) untilPhase(phase string) error {
	for {
		m, e := d.until("state")
		if e != nil {
			return e
		}
		if m.State == phase {
			return nil
		}
	}
}
func (d testDevice) mic(seq uint32, value int16) error {
	p := make([]int16, 320)
	for i := range p {
		p[i] = value
	}
	err := d.ws.Write(d.ctx, websocket.MessageBinary, encodeAudio(audioFrame{Kind: kindMicrophone, Epoch: d.epoch, Seq: seq, PCM: p}))
	// The board produces one frame per 20ms. An unpaced six-second recording
	// flooded the bounded receive queue in microseconds and made capture tests
	// fail with an unrelated socket overflow under scheduler/compile load.
	if err == nil {
		timer := time.NewTimer(time.Millisecond) // accelerated device cadence
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-d.ctx.Done():
			return d.ctx.Err()
		}
	}
	return err
}

func sessionHarness(t *testing.T, cfg Config, dep dependencies, scenario func(testDevice) error, timeout ...time.Duration) (chan Command, <-chan error, func() []State) {
	t.Helper()
	limit := 10 * time.Second
	if len(timeout) > 0 {
		limit = timeout[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	cmds := make(chan Command, 16)
	serverResult := make(chan error, 1)
	runResult := make(chan error, 1)
	var mu sync.Mutex
	var states []State
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/voice" {
			serverResult <- fmt.Errorf("bad path %s", r.URL.Path)
			return
		}
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			serverResult <- e
			return
		}
		defer ws.CloseNow()
		d := testDevice{ws: ws, ctx: ctx, epoch: 42}
		e = d.send(message{Type: "hello", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le", FrameSamples: 320, Capabilities: []string{"capture", "playback"}})
		if e == nil {
			m, readErr := d.until("config")
			e = readErr
			if e == nil && (!m.Enabled || m.Volume != cfg.Volume) {
				e = fmt.Errorf("bad config %+v", m)
			}
		}
		if e == nil {
			e = d.untilPhase("idle")
		}
		if e == nil {
			e = scenario(d)
		}
		serverResult <- e
	}))
	go func() {
		runResult <- runWithDependencies(ctx, strings.TrimPrefix(server.URL, "http://"), cfg, nil, cmds, func(s State) { mu.Lock(); states = append(states, s); mu.Unlock() }, dep)
	}()
	t.Cleanup(func() {
		cancel()
		server.Close()
		select {
		case <-runResult:
		case <-time.After(3 * time.Second):
			t.Error("Run did not release session")
		}
		select {
		case e := <-serverResult:
			if e != nil {
				t.Error("device scenario:", e)
			}
		case <-time.After(time.Second):
			t.Error("device scenario leaked")
		}
	})
	return cmds, serverResult, func() []State { mu.Lock(); defer mu.Unlock(); return append([]State{}, states...) }
}

func TestReferenceCreditsActualPlaybackAndDeferredInit(t *testing.T) {
	dep := fakeDependencies()
	dep.newDetector = func(Config) (detector, error) { return nil, errors.New("models missing") }
	dep.newVAD = func(Config) (vad, error) { return nil, errors.New("vad missing") }
	dep.newSpeech = func(Config) speechAPI { t.Error("reference initialized cloud"); return &fakeSpeech{} }
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) {
		t.Error("reference initialized brain")
		return nil, errors.New("unexpected")
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	holdAck := make(chan struct{})
	var cmds chan Command
	ready := make(chan struct{})
	cmds, _, snapshots := sessionHarness(t, Config{Enabled: true, Volume: 70}, dep, func(d testDevice) error {
		<-ready
		cmds <- Command{Kind: "reference"}
		start, e := d.until("playback_start")
		if e != nil {
			return e
		}
		if start.Samples != 641 {
			return errors.New("wrong declared samples")
		}
		// Wrong epoch and wrong turn must not add credits.
		d.send(message{Type: "credit", Epoch: 43, Turn: start.Turn, Frames: 32})
		d.send(message{Type: "credit", Turn: start.Turn + 1, Frames: 32})
		for seq := uint32(0); seq < 3; seq++ {
			if e = d.send(message{Type: "credit", Turn: start.Turn, Frames: 1}); e != nil {
				return e
			}
			typ, b, e := d.read()
			if e != nil {
				return e
			}
			if typ != websocket.MessageBinary {
				return fmt.Errorf("expected PCM with credit, got %s", b)
			}
			f, e := decodeAudio(b)
			if e != nil || f.Seq != seq || f.Turn != start.Turn {
				return fmt.Errorf("bad playback frame %+v %v", f, e)
			}
			if seq < 2 && len(f.PCM) != 320 || seq == 2 && len(f.PCM) != 1 {
				return errors.New("wrong final frame size")
			}
			if seq == 0 {
				if e = d.send(message{Type: "playing", Turn: start.Turn}); e != nil {
					return e
				}
				if e = d.untilPhase("speaking"); e != nil {
					return e
				}
			}
		}
		if _, e = d.until("playback_end"); e != nil {
			return e
		}
		close(holdAck)
		<-done
		if e = d.send(message{Type: "played", Turn: start.Turn}); e != nil {
			return e
		}
		if e = d.untilPhase("idle"); e != nil {
			return e
		}
		close(finished)
		return nil
	})
	close(ready)
	select {
	case <-holdAck:
	case <-time.After(3 * time.Second):
		t.Fatal("playback did not drain")
	}
	states := snapshots()
	last := states[len(states)-1]
	if last.Phase != "speaking" || last.WakeReady || !strings.Contains(last.Error, "models missing") {
		t.Fatalf("incorrect pre-ACK state %+v", last)
	}
	close(done)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("played ACK did not finish turn")
	}
}

func TestAutomaticWakeRetainsOnlyKeywordSuffixAndCompletesPipeline(t *testing.T) {
	dep := fakeDependencies()
	det := &fakeDetector{hitAt: 640, end: 320}
	v := &fakeVAD{}
	dep.newDetector = func(Config) (detector, error) { return det, nil }
	dep.newVAD = func(Config) (vad, error) { return v, nil }
	captured := make(chan []int16, 1)
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{recognize: func(_ context.Context, p []int16) (string, error) {
			captured <- append([]int16{}, p...)
			return "测试", nil
		}}
	}
	finished := make(chan struct{})
	_, _, _ = sessionHarness(t, Config{Enabled: true, MiMoAPIKey: "mimo", LLMAPIKey: "llm", LLMModel: "test", Volume: 60}, dep, func(d testDevice) error {
		if e := d.mic(0, 99); e != nil {
			return e
		}
		if e := d.mic(1, 7); e != nil {
			return e
		}
		if e := d.untilPhase("wake"); e != nil {
			return e
		}
		if e := d.untilPhase("listening"); e != nil {
			return e
		}
		for i := uint32(2); i < 42; i++ {
			if e := d.mic(i, 0); e != nil {
				return e
			}
		}
		transcript, e := d.until("transcript")
		if e != nil {
			return e
		}
		if transcript.Text != "测试" {
			return errors.New("wrong transcript")
		}
		response, e := d.until("response")
		if e != nil {
			return e
		}
		if response.Text != "现在八点。" {
			return errors.New("wrong response")
		}
		start, e := d.until("playback_start")
		if e != nil {
			return e
		}
		d.send(message{Type: "credit", Turn: start.Turn, Frames: 32})
		if _, e = d.until("playback_end"); e != nil {
			return e
		}
		d.send(message{Type: "playing", Turn: start.Turn})
		d.send(message{Type: "played", Turn: start.Turn})
		if e = d.untilPhase("idle"); e != nil {
			return e
		}
		close(finished)
		return nil
	})
	select {
	case p := <-captured:
		if len(p) != 41*320 || p[0] != 7 {
			t.Fatalf("keyword suffix lost/keyword included: len=%d first=%d", len(p), p[0])
		}
		for _, v := range p {
			if v == 99 {
				t.Fatal("keyword leaked to ASR")
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ASR not reached")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not finish")
	}
}

func TestMicGapCancelsCaptureAndRecovers(t *testing.T) {
	dep := fakeDependencies()
	dep.newSpeech = func(Config) speechAPI { t.Error("ASR called on interrupted recording"); return &fakeSpeech{} }
	ready := make(chan struct{})
	finished := make(chan struct{})
	var cmds chan Command
	cmds, _, _ = sessionHarness(t, Config{Enabled: true}, dep, func(d testDevice) error {
		<-ready
		cmds <- Command{Kind: "wake"}
		if e := d.untilPhase("listening"); e != nil {
			return e
		}
		d.mic(0, 1)
		d.mic(2, 1)
		m, e := d.until("cancel")
		if e != nil {
			return e
		}
		if m.Turn != 1 {
			return errors.New("wrong cancel turn")
		}
		eMsg, e := d.until("error")
		if e != nil {
			return e
		}
		if eMsg.Code != "mic_gap" {
			return fmt.Errorf("wrong error %+v", eMsg)
		}
		if e = d.untilPhase("error"); e != nil {
			return e
		}
		cmds <- Command{Kind: "wake"}
		if e = d.untilPhase("listening"); e != nil {
			return e
		}
		cmds <- Command{Kind: "cancel"}
		if e = d.untilPhase("idle"); e != nil {
			return e
		}
		close(finished)
		return nil
	})
	close(ready)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("gap recovery failed")
	}
}

func TestCancelSuppressesLateCloudResult(t *testing.T) {
	dep := fakeDependencies()
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{synthesize: func(context.Context, string, string) ([]int16, error) {
			close(started)
			<-release
			close(returned)
			return make([]int16, 320), nil
		}}
	}
	ready := make(chan struct{})
	finished := make(chan struct{})
	var cmds chan Command
	cmds, _, _ = sessionHarness(t, Config{Enabled: true, MiMoAPIKey: "secret"}, dep, func(d testDevice) error {
		<-ready
		cmds <- Command{Kind: "preview"}
		<-started
		cmds <- Command{Kind: "cancel"}
		if e := d.untilPhase("idle"); e != nil {
			return e
		}
		close(release)
		<-returned
		// A subsequent reference guarantees the stale preview result has been processed
		// before this distinct turn can start, without timing-based sleeps.
		cmds <- Command{Kind: "reference"}
		m, e := d.until("playback_start")
		if e != nil {
			return e
		}
		if m.Turn != 2 {
			return fmt.Errorf("stale cloud audio played on turn %d", m.Turn)
		}
		cmds <- Command{Kind: "cancel"}
		if e = d.untilPhase("idle"); e != nil {
			return e
		}
		close(finished)
		return nil
	})
	close(ready)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not settle")
	}
}
