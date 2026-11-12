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

func TestPlaybackOutputNormalizationAndJSON(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"", "agent"}, {"agent", "agent"}, {" AGENT ", "agent"}, {" Console ", "console"}} {
		var c Config
		b, _ := json.Marshal(map[string]string{"playbackOutput": tc.in})
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		got, err := NormalizeConfig(c)
		if err != nil || got.PlaybackOutput != tc.want {
			t.Fatalf("%q => %q, %v", tc.in, got.PlaybackOutput, err)
		}
		b, _ = json.Marshal(got)
		if !strings.Contains(string(b), `"playbackOutput":"`+tc.want+`"`) {
			t.Fatalf("missing JSON field: %s", b)
		}
	}
	for _, enabled := range []bool{false, true} {
		for _, value := range []string{"desktop", "both", "agent,console", "secret-value"} {
			if _, err := NormalizeConfig(Config{Enabled: enabled, PlaybackOutput: value}); err == nil || strings.Contains(err.Error(), value) {
				t.Fatalf("invalid output %q: %v", value, err)
			}
		}
	}
}

type fakeConsoleOutput struct {
	mu               sync.Mutex
	pcm              []int16
	limit            int
	finished, closed bool
	allowDrain       atomic.Bool
	err              error
}

func (o *fakeConsoleOutput) Write(p []int16) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return 0, errors.New("write to closed output")
	}
	if o.err != nil {
		return 0, o.err
	}
	n := len(p)
	if o.limit > 0 {
		n = min(n, o.limit)
	}
	o.pcm = append(o.pcm, p[:n]...)
	return n, nil
}
func (o *fakeConsoleOutput) Finish() { o.mu.Lock(); o.finished = true; o.mu.Unlock() }
func (o *fakeConsoleOutput) Status() (bool, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pcm) > 0, o.finished && o.allowDrain.Load(), o.err
}
func (o *fakeConsoleOutput) Close() { o.mu.Lock(); o.closed = true; o.mu.Unlock() }
func (o *fakeConsoleOutput) snapshot() ([]int16, bool, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int16(nil), o.pcm...), o.finished, o.closed
}

// Read all messages, not just desired controls: console must never send even a
// playback_start/end to firmware (nor a binary PCM frame).
func consoleUntil(d testDevice, want string) error {
	for {
		typ, b, err := d.read()
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			return errors.New("console PCM leaked to device")
		}
		m, err := decodeMessage(b)
		if err != nil {
			return err
		}
		if m.Type == "playback_start" || m.Type == "playback_end" {
			return fmt.Errorf("console sent %s to device", m.Type)
		}
		if m.Type == "error" && want != "error" {
			return fmt.Errorf("unexpected voice error: %s", m.Message)
		}
		if m.Type == want || m.Type == "state" && m.State == want {
			return nil
		}
	}
}

func TestDefaultAgentPlaybackDoesNotOpenConsole(t *testing.T) {
	dep := fakeDependencies()
	dep.newOutput = func(Config) (consoleOutput, error) {
		t.Error("agent route opened console output")
		return nil, errors.New("unexpected host output")
	}
	ready, done := make(chan struct{}), make(chan struct{})
	var commands chan Command
	commands, _, _ = sessionHarness(t, Config{Enabled: true}, dep, func(d testDevice) error {
		defer close(done)
		<-ready
		commands <- Command{Kind: "reference"}
		start, err := d.until("playback_start")
		if err != nil {
			return err
		}
		if err := drainSegment(t, d, start, 0); err != nil {
			return err
		}
		return d.untilPhase("idle")
	})
	close(ready)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("default agent route stalled")
	}
}

func TestFishConsoleDoesNotRequireStreamingFirmware(t *testing.T) {
	for _, destination := range []string{"agent", "console"} {
		t.Run(destination, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				d := testDevice{ws: ws, ctx: ctx, epoch: 42}
				_ = d.send(message{Type: "hello", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le", FrameSamples: 320, Capabilities: []string{"capture", "playback"}})
				for {
					if _, _, err := d.read(); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			connected := false
			err := runWithDependencies(ctx, strings.TrimPrefix(server.URL, "http://"), Config{Enabled: true, TTSProvider: "fish", PlaybackOutput: destination}, nil, nil, func(s State) {
				if s.Connected {
					connected = true
					cancel()
				}
			}, fakeDependencies())
			if destination == "console" {
				if !connected || !errors.Is(err, context.Canceled) {
					t.Fatalf("console rejected old firmware: connected=%v err=%v", connected, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "Fish") || connected {
				t.Fatalf("agent accepted old firmware: %v", err)
			}
		})
	}
}

func TestConsoleSegmentsPlayBeforeGenerationEndsAndKeepProgress(t *testing.T) {
	dep := fakeDependencies()
	release, exited := make(chan struct{}), make(chan struct{})
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) {
		return &sessionStreamingBrain{release: release, exited: exited}, nil
	}
	created := make(chan *fakeConsoleOutput, 4)
	dep.newOutput = func(Config) (consoleOutput, error) { o := &fakeConsoleOutput{}; created <- o; return o, nil }
	var synth atomic.Int32
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{synthesize: func(context.Context, string, string) ([]int16, error) {
			value := int16(synth.Add(1))
			return []int16{value, value, value}, nil
		}}
	}
	ready, done := make(chan struct{}), make(chan struct{})
	var commands chan Command
	commands, _, snapshots := sessionHarness(t, Config{Enabled: true, PlaybackOutput: "console", MiMoAPIKey: "key", LLMModel: "model"}, dep, func(d testDevice) error {
		defer close(done)
		<-ready
		commands <- Command{Kind: "text", Text: "hello"}
		if err := consoleUntil(d, "speaking"); err != nil {
			return err
		}
		first := <-created
		first.allowDrain.Store(true)
		if err := consoleUntil(d, "synthesizing"); err != nil {
			return err
		}
		select {
		case <-exited:
			return errors.New("generation ended before first segment played")
		default:
		}
		pcm, _, closed := first.snapshot()
		if !closed || len(pcm) != 3 || pcm[0] != 1 {
			return errors.New("first segment not rendered and closed")
		}
		close(release)
		if err := consoleUntil(d, "speaking"); err != nil {
			return err
		}
		second := <-created
		second.allowDrain.Store(true)
		if err := consoleUntil(d, "idle"); err != nil {
			return err
		}
		pcm, _, closed = second.snapshot()
		if !closed || len(pcm) != 3 || pcm[0] != 2 {
			return errors.New("second segment out of order")
		}
		return nil
	})
	close(ready)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("segments stalled")
	}
	found := false
	for _, s := range snapshots() {
		if s.Phase == "idle" && s.GeneratedSegments == 2 && s.PlayedSegments == 2 && s.Response != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing segmented completion: %+v", snapshots())
	}
}

func TestConsoleCancelClosesOutputAndFishGeneration(t *testing.T) {
	dep := fakeDependencies()
	output := &fakeConsoleOutput{limit: 97}
	dep.newOutput = func(Config) (consoleOutput, error) { return output, nil }
	exited := make(chan struct{})
	dep.newSpeech = func(Config) speechAPI {
		return &streamTestSpeech{stream: func(ctx context.Context, in <-chan string, audio func([]int16) error) error {
			defer close(exited)
			<-in
			if err := audio(make([]int16, frameSamples*3)); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}}
	}
	cfg := fishStreamTestConfig()
	cfg.PlaybackOutput = "console"
	ready, done := make(chan struct{}), make(chan struct{})
	var commands chan Command
	commands, _, snapshots := sessionHarness(t, cfg, dep, func(d testDevice) error {
		defer close(done)
		<-ready
		commands <- Command{Kind: "preview"}
		if err := consoleUntil(d, "speaking"); err != nil {
			return err
		}
		commands <- Command{Kind: "cancel"}
		if err := consoleUntil(d, "idle"); err != nil {
			return err
		}
		select {
		case <-exited:
		case <-d.ctx.Done():
			return errors.New("cancel did not stop Fish generation")
		}
		_, _, closed := output.snapshot()
		if !closed {
			return errors.New("cancel left host output open")
		}
		return nil
	})
	close(ready)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel stalled")
	}
	for _, s := range snapshots() {
		if s.PlayedSegments != 0 {
			t.Fatal("cancelled stream counted as played")
		}
	}
}

func TestConsoleOutputErrorsNeverFallbackToAgent(t *testing.T) {
	for _, stage := range []string{"open", "write"} {
		t.Run(stage, func(t *testing.T) {
			dep := fakeDependencies()
			output := &fakeConsoleOutput{err: errors.New("native output failed")}
			dep.newOutput = func(Config) (consoleOutput, error) {
				if stage == "open" {
					return nil, errors.New("native output failed")
				}
				return output, nil
			}
			ready, done := make(chan struct{}), make(chan struct{})
			var commands chan Command
			commands, _, snapshots := sessionHarness(t, Config{Enabled: true, PlaybackOutput: "console"}, dep, func(d testDevice) error {
				defer close(done)
				<-ready
				commands <- Command{Kind: "reference"}
				if err := consoleUntil(d, "error"); err != nil {
					return err
				}
				return consoleUntil(d, "error")
			})
			close(ready)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("output failure stalled")
			}
			if stage == "write" {
				_, _, closed := output.snapshot()
				if !closed {
					t.Fatal("failed output not closed")
				}
			}
			found := false
			for _, s := range snapshots() {
				if s.Phase == "error" && strings.Contains(s.Error, "native output failed") {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing playback diagnostic: %+v", snapshots())
			}
		})
	}
}

func TestConsoleRoutesReferencePreviewAndFishWithoutDeviceCredits(t *testing.T) {
	for _, kind := range []string{"reference", "preview", "fish"} {
		t.Run(kind, func(t *testing.T) {
			dep := fakeDependencies()
			output := &fakeConsoleOutput{limit: 97}
			dep.newOutput = func(c Config) (consoleOutput, error) {
				if c.Volume != 63 {
					t.Errorf("volume=%d", c.Volume)
				}
				return output, nil
			}
			want := make([]int16, 641)
			for i := range want {
				want[i] = int16(i - 320)
			}
			dep.reference = func(string) ([]int16, error) { return want, nil }
			dep.newSpeech = func(Config) speechAPI {
				return &fakeSpeech{synthesize: func(context.Context, string, string) ([]int16, error) { return want, nil }}
			}
			cfg := Config{Enabled: true, PlaybackOutput: "console", Volume: 63, MiMoAPIKey: "key"}
			command := kind
			if kind == "fish" {
				command = "preview"
				cfg.TTSProvider = "fish"
				cfg.FishAPIKey = "key"
				cfg.FishReferenceID = "id"
				dep.newSpeech = func(Config) speechAPI {
					return &streamTestSpeech{stream: func(ctx context.Context, in <-chan string, audio func([]int16) error) error { <-in; return audio(want) }}
				}
			}
			ready, done := make(chan struct{}), make(chan struct{})
			var commands chan Command
			commands, _, snapshots := sessionHarness(t, cfg, dep, func(d testDevice) error {
				defer close(done)
				<-ready
				commands <- Command{Kind: command}
				if err := consoleUntil(d, "speaking"); err != nil {
					return err
				}
				// Unsolicited/stale device ACKs cannot finish console playback.
				for _, m := range []message{{Type: "credit", Turn: 1, Frames: 64}, {Type: "playing", Turn: 1}, {Type: "played", Turn: 1}} {
					if err := d.send(m); err != nil {
						return err
					}
				}
				output.allowDrain.Store(true)
				return consoleUntil(d, "idle")
			})
			close(ready)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("console playback stalled without credits")
			}
			pcm, finished, closed := output.snapshot()
			if !finished || !closed || len(pcm) != len(want) {
				t.Fatalf("output samples=%d finished=%v closed=%v", len(pcm), finished, closed)
			}
			for i := range pcm {
				if pcm[i] != want[i] {
					t.Fatalf("sample %d mismatch", i)
				}
			}
			found := false
			for _, s := range snapshots() {
				if s.Turn == 1 && s.Phase == "idle" && s.GeneratedSegments == 1 && s.PlayedSegments == 1 && s.FirstAudioMillis > 0 {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing completed progress: %+v", snapshots())
			}
		})
	}
}
