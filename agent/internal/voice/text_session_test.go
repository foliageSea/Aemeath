package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
)

func TestTextConversationUsesAgentSpeechAndSharedHistory(t *testing.T) {
	requests := make(chan []map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Messages []map[string]any }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests <- req.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"回答。\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	s := recordTestSession(t)
	s.generation = 0
	s.results = make(chan result, 16)
	memoryPath := filepath.Join(t.TempDir(), "history.json")
	memory := brain.OpenMemory(memoryPath)
	dep := fakeDependencies()
	// Fish text turns need no ASR key and reuse the same conversation engine.
	cfg := Config{TTSProvider: "fish", FishAPIKey: "fish", FishReferenceID: "reference", LLMModel: "test"}
	dep.newBrain = func(ctx context.Context, _ Config, _ Host) (brainAPI, error) {
		return brain.New(ctx, brain.Config{BaseURL: server.URL, Model: "test", Memory: memory}, nil)
	}
	recognized := 0
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{
			recognize: func(context.Context, []int16) (string, error) { recognized++; return "语音问题", nil },
			synthesize: func(_ context.Context, text, _ string) ([]int16, error) {
				if text != "回答。" {
					return nil, fmt.Errorf("unexpected speech %q", text)
				}
				return []int16{1, 2}, nil
			},
		}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker(ctx, cfg, memoryTestHost{memory: memory}, dep, s.jobs, s.results) }()
	defer func() {
		cancel()
		if s.cancelTurn != nil {
			s.cancelTurn()
		}
		<-done
	}()
	runTurn := func(text string) {
		t.Helper()
		if err := s.command(Command{Kind: "text", Text: " " + text + "\n"}); err != nil {
			t.Fatal(err)
		}
		if s.state.Phase != "thinking" || s.state.Transcript != text || s.state.ASRMillis != 0 || s.capture != nil {
			t.Fatalf("text state: %+v", s.state)
		}
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case r := <-s.results:
				if r.phase == "recognizing" || r.phase == "wake_only" {
					t.Fatalf("text entered ASR: %+v", r)
				}
				if err := s.result(r); err != nil {
					t.Fatal(err)
				}
				if r.err != nil {
					t.Fatal(r.err)
				}
				if r.phase == "generation_done" {
					if s.state.Response != "回答。" || s.state.GeneratedSegments != 1 || s.play == nil {
						t.Fatalf("conversation did not reach playback: %+v", s.state)
					}
					return
				}
			case <-deadline.C:
				t.Fatal("text worker stalled")
			}
		}
	}
	for i, text := range []string{"爱弥斯，请记住我的名字", "我叫什么"} {
		runTurn(text)
		msgs := <-requests
		if len(msgs) != 2+2*i || msgs[len(msgs)-1]["content"] != text {
			t.Fatalf("history request: %+v", msgs)
		}
	}
	if recognized != 0 {
		t.Fatalf("ASR called %d times", recognized)
	}
	turns := brain.OpenMemory(memoryPath).Turns()
	if len(turns) != 2 || turns[0].User != "爱弥斯，请记住我的名字" || turns[1].User != "我叫什么" || turns[1].Assistant != "回答。" {
		t.Fatalf("persisted history: %+v", turns)
	}
}

type textCancelBrain struct{ entered, canceled chan struct{} }

func (b *textCancelBrain) Clear() {}
func (b *textCancelBrain) Reply(ctx context.Context, text string) (string, error) {
	if text == "first" {
		close(b.entered)
		<-ctx.Done()
		close(b.canceled)
		return "obsolete answer", nil
	}
	return "新回答。", nil
}

func TestTextReplacesActiveTurnAndGuardsStaleResults(t *testing.T) {
	s := recordTestSession(t)
	s.generation = 0
	s.results = make(chan result, 32)
	engine := &textCancelBrain{entered: make(chan struct{}), canceled: make(chan struct{})}
	dep := fakeDependencies()
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { return engine, nil }
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker(ctx, Config{MiMoAPIKey: "key", LLMModel: "test"}, nil, dep, s.jobs, s.results)
	}()
	defer func() {
		cancel()
		if s.cancelTurn != nil {
			s.cancelTurn()
		}
		<-done
	}()
	if err := s.command(Command{Kind: "text", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	oldTurn := s.state.Turn
	select {
	case <-engine.entered:
	case <-time.After(time.Second):
		t.Fatal("agent did not start")
	}
	if err := s.command(Command{Kind: "text", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.canceled:
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel agent")
	}
	assertStale := func(turn uint32) {
		t.Helper()
		before := s.state.Clone()
		for _, r := range []result{
			{phase: "transcript", text: "stale"}, {phase: "response", text: "stale"},
			{phase: "thinking", err: errors.New("stale error")},
			{phase: "audio_segment", pcm: []int16{9}, final: true},
			{phase: "audio_stream_start"}, {phase: "generation_done"},
			{toolCall: &brain.ToolCall{ID: 1, Status: "running"}},
			{phase: "response", context: &brain.ContextStats{EstimatedTokens: 999}},
		} {
			r.turn, r.generation = turn, s.generation
			if err := s.result(r); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(before, s.state) {
			t.Fatalf("stale results changed state: before=%+v after=%+v", before, s.state)
		}
	}
	assertStale(oldTurn)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for !s.generationDone {
		select {
		case r := <-s.results:
			if err := s.result(r); err != nil {
				t.Fatal(err)
			}
			if r.turn == s.state.Turn && r.err != nil {
				t.Fatal(r.err)
			}
		case <-timer.C:
			t.Fatal("replacement stalled")
		}
	}
	if s.state.Response != "新回答。" {
		t.Fatalf("replacement response: %+v", s.state)
	}
	if err := s.command(Command{Kind: "cancel"}); err != nil {
		t.Fatal(err)
	}
	assertStale(s.state.Turn)
	if s.play != nil || len(s.queuedAudio) != 0 || s.active {
		t.Fatal("cancel left playback active")
	}
}

func TestTextStopsRecordingAndIgnoresLateRelease(t *testing.T) {
	s := recordTestSession(t)
	recordCommand(t, s, "record_start")
	recordFrame(t, s, 7)
	if err := s.command(Command{Kind: "text", Text: "typed question"}); err != nil {
		t.Fatal(err)
	}
	defer s.cancelTurn()
	recordCommand(t, s, "record_stop")
	w := nextRecordWork(t, s)
	if w.kind != "text" || w.text != "typed question" || len(w.pcm) != 0 || s.capture != nil || s.recording {
		t.Fatalf("recording not replaced: %+v", w)
	}
	noRecordWork(t, s)
}
