package voiceapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMiMoExactASRAndCloneContract(t *testing.T) {
	reference := writeReference(t, fixtureWAV(48000, 1, 24, make([]int32, 4800)))
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("headers missing")
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if req["stream"] != false {
			t.Error("stream must be false")
		}
		messages := req["messages"].([]any)
		if len(messages) != 1 {
			t.Error("unexpected messages")
		}
		m := messages[0].(map[string]any)
		switch calls {
		case 1:
			if req["model"] != "mimo-v2.5-asr" || m["role"] != "user" || req["asr_options"].(map[string]any)["language"] != "zh" {
				t.Errorf("bad ASR contract %v", req)
			}
			parts := m["content"].([]any)
			part := parts[0].(map[string]any)
			audio := part["input_audio"].(map[string]any)
			if part["type"] != "input_audio" || audio["format"] != "wav" {
				t.Error("bad input_audio")
			}
			pcm := decodeDataWAV(t, audio["data"].(string))
			if !reflect.DeepEqual(pcm, []int16{-32768, 0, 32767}) {
				t.Errorf("ASR samples %v", pcm)
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"你好，小爱"}}]}`)
		case 2:
			if req["model"] != "mimo-v2.5-tts-voiceclone" || m["role"] != "assistant" || m["content"] != "你好" {
				t.Errorf("bad TTS contract %v", req)
			}
			audio := req["audio"].(map[string]any)
			if audio["format"] != "wav" {
				t.Error("not WAV")
			}
			pcm := decodeDataWAV(t, audio["voice"].(string))
			if len(pcm) != 1600 {
				t.Errorf("converted reference=%d", len(pcm))
			}
			encoded := base64.StdEncoding.EncodeToString(fixtureWAV(48000, 2, 24, make([]int32, 4800*2)))
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"audio": map[string]any{"data": encoded}}}}})
		default:
			t.Error("unexpected call")
		}
	}))
	defer s.Close()
	c := New(s.URL+"/v1", "test-secret")
	text, err := c.Recognize(context.Background(), []int16{-32768, 0, 32767})
	if err != nil || text != "你好，小爱" {
		t.Fatalf("%q %v", text, err)
	}
	pcm, err := c.Synthesize(context.Background(), "你好", reference)
	if err != nil || len(pcm) != 1600 {
		t.Fatalf("TTS len=%d err=%v", len(pcm), err)
	}
}
func decodeDataWAV(t *testing.T, s string) []int16 {
	t.Helper()
	const prefix = "data:audio/wav;base64,"
	if !strings.HasPrefix(s, prefix) {
		t.Error("missing data URL")
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		t.Error(err)
		return nil
	}
	if binary.LittleEndian.Uint16(b[34:]) != 16 || binary.LittleEndian.Uint32(b[24:]) != 16000 {
		t.Error("expected normalized PCM16/16k")
	}
	pcm, err := decodeWAV(b)
	if err != nil {
		t.Error(err)
	}
	return pcm
}

func TestSafeRetriesAndCredentialRedaction(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			io.WriteString(w, "test-secret Authorization Bearer")
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer s.Close()
	c := New(s.URL, "test-secret")
	got, err := c.Recognize(context.Background(), []int16{1})
	if err != nil || got != "ok" || calls.Load() != 2 {
		t.Fatalf("%s %v calls=%d", got, err, calls.Load())
	}
	for _, status := range []int{400, 401, 500, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var n atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				w.Header().Set("Location", s.URL)
				w.WriteHeader(status)
				io.WriteString(w, "test-secret")
			}))
			defer server.Close()
			_, err := New(server.URL, "test-secret").Recognize(context.Background(), []int16{1})
			if err == nil || strings.Contains(err.Error(), "test-secret") || n.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, n.Load())
			}
		})
	}
}
func TestCanceledRequestAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "retry"}[retry], func(t *testing.T) {
			entered := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(entered)
				if retry {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(429)
				} else {
					<-r.Context().Done()
				}
			}))
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := New(s.URL, "secret").Recognize(ctx, []int16{1}); done <- err }()
			<-entered
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation stalled")
			}
		})
	}
}
func TestBoundedAndInvalidResponses(t *testing.T) {
	for name, body := range map[string]string{"bad json": "secret invalid", "no choices": `{"choices":[]}`, "provider error": `{"error":{"message":"secret"}}`, "too large": strings.Repeat("x", maxResponseBytes+1)} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, body) }))
			defer s.Close()
			_, err := New(s.URL, "secret").Recognize(context.Background(), []int16{1})
			if err == nil || strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
	for _, base := range []string{"http://user:secret@localhost", "http://localhost?key=secret", ":bad"} {
		_, err := New(base, "secret").Recognize(context.Background(), []int16{1})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe URL error %v", err)
		}
	}
}
func TestTTSRejectsMalformedAudioAndCanceledInput(t *testing.T) {
	ref := writeReference(t, EncodeWAV([]int16{1}, 16000))
	for _, data := range []string{"", "!!!!", "data:audio/mpeg;base64,AAAA", base64.StdEncoding.EncodeToString([]byte("not a WAV"))} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"audio": map[string]string{"data": data}}}}})
		}))
		_, err := New(s.URL, "secret").Synthesize(context.Background(), "hello", ref)
		s.Close()
		if err == nil {
			t.Errorf("accepted %q", data)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New("", "").Synthesize(ctx, "hello", ref); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
