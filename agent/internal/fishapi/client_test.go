package fishapi

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/voiceapi"
	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

func mockFish(t *testing.T, serve func(context.Context, *websocket.Conn)) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/tts/live" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("model") != "s2.1-pro" {
			t.Errorf("unexpected Fish handshake %s model=%s", r.URL.Path, r.Header.Get("model"))
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		serve(ctx, conn)
	}))
	t.Cleanup(s.Close)
	c, err := New(Config{BaseURL: s.URL, APIKey: "test-secret", ReferenceID: " voice-id ", ReferenceAudio: "/does/not/exist.wav"})
	if err != nil {
		t.Fatal(err)
	}
	c.http = s.Client()
	return c, &calls
}
func readEvent(t *testing.T, ctx context.Context, c *websocket.Conn) (event, bool) {
	t.Helper()
	kind, b, err := c.Read(ctx)
	if err != nil {
		t.Error(err)
		return event{}, false
	}
	if kind != websocket.MessageBinary {
		t.Error("request was not binary")
	}
	var e event
	if err := msgpack.Unmarshal(b, &e); err != nil {
		t.Error(err)
		return event{}, false
	}
	return e, true
}
func expectEvent(t *testing.T, ctx context.Context, c *websocket.Conn, want string) (event, bool) {
	t.Helper()
	e, ok := readEvent(t, ctx, c)
	if ok && e.Event != want {
		t.Errorf("event=%s want=%s", e.Event, want)
		return e, false
	}
	return e, ok
}
func sendEvent(t *testing.T, ctx context.Context, c *websocket.Conn, v any) bool {
	t.Helper()
	b, err := msgpack.Marshal(v)
	if err != nil {
		t.Error(err)
		return false
	}
	if err = c.Write(ctx, websocket.MessageBinary, b); err != nil {
		t.Error(err)
		return false
	}
	return true
}
func sendAudio(t *testing.T, ctx context.Context, c *websocket.Conn, b []byte) bool {
	return sendEvent(t, ctx, c, map[string]any{"event": "audio", "audio": b})
}
func finish(t *testing.T, ctx context.Context, c *websocket.Conn) bool {
	return sendEvent(t, ctx, c, map[string]string{"event": "finish", "reason": "stop"})
}
func consumeInput(t *testing.T, ctx context.Context, c *websocket.Conn) bool {
	if _, ok := expectEvent(t, ctx, c, "start"); !ok {
		return false
	}
	for {
		e, ok := readEvent(t, ctx, c)
		if !ok {
			return false
		}
		if e.Event == "stop" {
			return true
		}
		if e.Event != "text" && e.Event != "flush" {
			t.Errorf("unexpected event=%s", e.Event)
			return false
		}
	}
}
func textInput(s ...string) <-chan string {
	ch := make(chan string, len(s))
	for _, v := range s {
		ch <- v
	}
	close(ch)
	return ch
}
func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
func waitStream(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop")
		return nil
	}
}
func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func assertSafe(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"test-secret", "Authorization", "Bearer", "raw-server-body"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe error=%v", err)
		}
	}
}

func TestBidirectionalAudioBeforeInputEOFOddPCMAndReferenceID(t *testing.T) {
	c, calls := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
		start, ok := expectEvent(t, ctx, conn, "start")
		if !ok {
			return
		}
		r := start.Request
		if r == nil || r.Text != "" || r.Format != "pcm" || r.SampleRate != 16000 || r.Latency != "balanced" || r.ReferenceID != "voice-id" || len(r.References) != 0 {
			t.Errorf("start=%+v", r)
		}
		e, ok := expectEvent(t, ctx, conn, "text")
		if !ok {
			return
		}
		if e.Text != "第一句。" {
			t.Errorf("text=%q", e.Text)
		}
		if _, ok = expectEvent(t, ctx, conn, "flush"); !ok {
			return
		}
		if !sendEvent(t, ctx, conn, map[string]any{"event": "future_extension", "metadata": map[string]any{"nested": []any{true, 123, "ignore"}}}) {
			return
		}
		if !sendAudio(t, ctx, conn, []byte{0}) || !sendAudio(t, ctx, conn, []byte{128, 255, 127, 1}) {
			return
		}
		e, ok = expectEvent(t, ctx, conn, "text")
		if !ok {
			return
		}
		if e.Text != "第二句。" {
			t.Errorf("tail=%q", e.Text)
		}
		if _, ok = expectEvent(t, ctx, conn, "flush"); !ok {
			return
		}
		if _, ok = expectEvent(t, ctx, conn, "stop"); !ok {
			return
		}
		if !sendAudio(t, ctx, conn, []byte{0}) {
			return
		}
		finish(t, ctx, conn)
	})
	input := make(chan string, 2)
	input <- "第一句。"
	heard := make(chan struct{})
	var once sync.Once
	var chunks [][]int16
	done := make(chan error, 1)
	go func() {
		done <- c.Stream(testContext(t), input, func(p []int16) error { chunks = append(chunks, p); once.Do(func() { close(heard) }); return nil })
	}()
	waitSignal(t, heard, "audio before text EOF")
	select {
	case err := <-done:
		t.Fatalf("finished before EOF: %v", err)
	default:
	}
	input <- "第二句。"
	close(input)
	if err := waitStream(t, done); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chunks, [][]int16{{-32768, 32767}, {1}}) {
		t.Fatalf("PCM boundaries=%v", chunks)
	}
	if calls.Load() != 1 {
		t.Fatal("session replayed")
	}
}

func TestInlineReferenceUsesWAVBytesAndExactTranscript(t *testing.T) {
	const transcript = " 你好，小爱。 \n"
	c, _ := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
		e, ok := expectEvent(t, ctx, conn, "start")
		if !ok {
			return
		}
		r := e.Request
		if r == nil || r.ReferenceID != "" || len(r.References) != 1 {
			t.Errorf("reference=%+v", r)
			return
		}
		ref := r.References[0]
		if ref.Text != transcript || string(ref.Audio[:4]) != "RIFF" || binary.LittleEndian.Uint32(ref.Audio[24:]) != 16000 || binary.LittleEndian.Uint16(ref.Audio[34:]) != 16 {
			t.Errorf("invalid normalized inline reference")
		}
		for {
			e, ok = readEvent(t, ctx, conn)
			if !ok {
				return
			}
			if e.Event == "stop" {
				break
			}
		}
		if sendAudio(t, ctx, conn, []byte{1, 0}) {
			finish(t, ctx, conn)
		}
	})
	path := filepath.Join(t.TempDir(), "reference.wav")
	if err := os.WriteFile(path, voiceapi.EncodeWAV(make([]int16, 160), 16000), 0600); err != nil {
		t.Fatal(err)
	}
	c.cfg.ReferenceID = ""
	c.cfg.ReferenceAudio = path
	c.cfg.ReferenceText = transcript
	if err := c.Stream(testContext(t), textInput("你好"), func([]int16) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestEarlyFinishAndEOFNeverSucceedOrReplay(t *testing.T) {
	for _, mode := range []string{"premature_finish", "EOF_without_finish", "finish_error", "odd_final_sample", "empty_audio", "error_event"} {
		t.Run(mode, func(t *testing.T) {
			c, calls := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
				if mode == "premature_finish" {
					if _, ok := expectEvent(t, ctx, conn, "start"); ok {
						finish(t, ctx, conn)
					}
					return
				}
				if !consumeInput(t, ctx, conn) {
					return
				}
				switch mode {
				case "EOF_without_finish":
					sendAudio(t, ctx, conn, []byte{1, 0})
				case "finish_error":
					sendEvent(t, ctx, conn, map[string]string{"event": "finish", "reason": "error", "message": "test-secret raw-server-body"})
				case "odd_final_sample":
					if sendAudio(t, ctx, conn, []byte{1}) {
						finish(t, ctx, conn)
					}
				case "empty_audio":
					finish(t, ctx, conn)
				case "error_event":
					sendEvent(t, ctx, conn, map[string]string{"event": "error", "message": "Authorization Bearer test-secret raw-server-body"})
				}
			})
			input := textInput("hello")
			if mode == "premature_finish" {
				input = make(chan string)
			}
			err := c.Stream(testContext(t), input, func([]int16) error { return nil })
			assertSafe(t, err)
			if calls.Load() != 1 {
				t.Fatalf("requests=%d", calls.Load())
			}
		})
	}
}

func TestCancellationAndCallbackFailureCloseBothDirections(t *testing.T) {
	for _, mode := range []string{"cancel_waiting_input", "callback_error", "cancel_blocked_callback"} {
		t.Run(mode, func(t *testing.T) {
			connected := make(chan struct{})
			serverExited := make(chan struct{})
			callbackEntered := make(chan struct{})
			c, _ := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
				defer close(serverExited)
				if _, ok := expectEvent(t, ctx, conn, "start"); !ok {
					return
				}
				close(connected)
				if mode != "cancel_waiting_input" {
					if !sendAudio(t, ctx, conn, []byte{1, 0}) {
						return
					}
				}
				// Read until the client tears down; the sender is still blocked on open input.
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						return
					}
				}
			})
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			input := make(chan string)
			done := make(chan error, 1)
			go func() {
				done <- c.Stream(ctx, input, func([]int16) error {
					close(callbackEntered)
					if mode == "cancel_blocked_callback" {
						<-ctx.Done()
						return ctx.Err()
					}
					return errors.New("test-secret callback failure")
				})
			}()
			waitSignal(t, connected, "connection")
			if mode == "cancel_waiting_input" {
				cancel()
			} else {
				waitSignal(t, callbackEntered, "callback")
				if mode == "cancel_blocked_callback" {
					cancel()
				}
			}
			err := waitStream(t, done)
			if mode == "callback_error" {
				assertSafe(t, err)
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel=%v", err)
			}
			waitSignal(t, serverExited, "server teardown")
			// Stream joined its sender, so it can no longer receive input after return.
			select {
			case input <- "obsolete":
				t.Fatal("sender still running after return")
			default:
			}
		})
	}
}

func TestPCMAndTextLimits(t *testing.T) {
	t.Run("PCM120seconds", func(t *testing.T) {
		c, _ := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
			if !consumeInput(t, ctx, conn) {
				return
			}
			chunk := make([]byte, 32000)
			for i := 0; i < 120; i++ {
				if !sendAudio(t, ctx, conn, chunk) {
					return
				}
			}
			if sendAudio(t, ctx, conn, []byte{1, 0}) { /* overflow closes the session */
			}
		})
		n := 0
		err := c.Stream(testContext(t), textInput("hello"), func(p []int16) error { n += len(p); return nil })
		assertSafe(t, err)
		if n != maxSamples {
			t.Fatalf("accepted=%d want=%d", n, maxSamples)
		}
	})
	t.Run("text16KiB", func(t *testing.T) {
		var texts atomic.Int32
		c, _ := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
			for {
				e, ok := readNoError(ctx, conn)
				if !ok {
					return
				}
				if e.Event == "text" {
					texts.Add(1)
				}
			}
		})
		err := c.Stream(testContext(t), textInput(strings.Repeat("x", maxTextBytes+1)), func([]int16) error { return nil })
		assertSafe(t, err)
		if texts.Load() != 0 {
			t.Fatal("oversized text sent")
		}
	})
}
func readNoError(ctx context.Context, c *websocket.Conn) (event, bool) {
	_, b, err := c.Read(ctx)
	if err != nil {
		return event{}, false
	}
	var e event
	err = msgpack.Unmarshal(b, &e)
	return e, err == nil
}

func TestUTF8ChunksAndValidation(t *testing.T) {
	c, _ := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
		if _, ok := expectEvent(t, ctx, conn, "start"); !ok {
			return
		}
		e, ok := expectEvent(t, ctx, conn, "text")
		if !ok {
			return
		}
		if e.Text != "你" {
			t.Errorf("split UTF8=%q", e.Text)
		}
		if _, ok = expectEvent(t, ctx, conn, "flush"); !ok {
			return
		}
		if _, ok = expectEvent(t, ctx, conn, "stop"); !ok {
			return
		}
		if sendAudio(t, ctx, conn, []byte{1, 0}) {
			finish(t, ctx, conn)
		}
	})
	b := []byte("你")
	if err := c.Stream(testContext(t), textInput(string(b[:1]), string(b[1:])), func([]int16) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{}, {APIKey: "test-secret", ReferenceID: "id", BaseURL: "https://u:test-secret@host"}, {APIKey: "test-secret", ReferenceID: "id", BaseURL: "https://host?token=test-secret"}, {APIKey: "test-secret", ReferenceID: "id", Latency: "invalid"}, {APIKey: "test-secret", ReferenceAudio: "x"}} {
		_, err := New(cfg)
		assertSafe(t, err)
	}
}

func TestDialErrorsAreRedactedAndNotRetried(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "Authorization Bearer test-secret raw-server-body", 401)
	}))
	defer s.Close()
	c, err := New(Config{BaseURL: s.URL, APIKey: "test-secret", ReferenceID: "id"})
	if err != nil {
		t.Fatal(err)
	}
	assertSafe(t, c.Stream(testContext(t), textInput("hello"), func([]int16) error { return nil }))
	if requests.Load() != 1 {
		t.Fatal("dial retried")
	}
}
