package voice

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/replytext"
)

func TestVoiceProtocolGuardRejectsBeforeSpeechAndDisplay(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, response := range []string{
			`<｜DSML｜function_calls><｜DSML｜invoke name="secret">`,
			"<tool_search>secret</tool_search>",
			"<tool_call>secret</tool_call>",
			"<function_calls>secret</function_calls>",
			"[1] <tool_call>secret</tool_call>", // Must be checked before citation removal.
			"<tool_",                            // Finish rejects an incomplete reserved prefix.
		} {
			for split := 0; split <= len(response); split++ {
				engine := &pipelineTestBrain{stream: func(_ context.Context, _ string, delta func(string) error) (string, error) {
					for _, part := range []string{response[:split], response[split:]} {
						if err := delta(part); err != nil {
							return "", err
						}
					}
					return response, nil
				}}
				wav := &pipelineTestSpeech{synth: func(context.Context, string, string, int) ([]int16, error) { return []int16{1}, nil }}
				fish := &streamTestSpeech{stream: func(context.Context, <-chan string, func([]int16) error) error { return nil }}
				var speech speechAPI = wav
				if streaming {
					speech = fish
				}
				var events pipelineTestEvents
				err := segmentedSpeech(pipelineTestContext(t), engine, speech, "question", "", events.emit)
				if !errors.Is(err, replytext.ErrProtocol) {
					t.Fatalf("stream=%v split=%d: %v", streaming, split, err)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("protocol arguments leaked in error")
				}
				if wav.calls.Load() != 0 || fish.calls.Load() != 0 || fish.synthCalls.Load() != 0 {
					t.Fatal("protocol reached TTS")
				}
				if engine.streamCalls.Load() != 1 || engine.replyCalls.Load() != 0 {
					t.Fatal("protocol caused an automatic retry")
				}
				for _, r := range events.snapshot() {
					if r.phase == "response" && (r.final || strings.TrimSpace(r.text) != "") {
						t.Fatalf("protocol entered response: %q", r.text)
					}
				}
			}
		}
	}
}

func TestVoiceProtocolGuardValidatesFinalAndIgnoredDeltaFailure(t *testing.T) {
	for _, ignoreDeltaError := range []bool{false, true} {
		engine := &pipelineTestBrain{stream: func(_ context.Context, _ string, delta func(string) error) (string, error) {
			if ignoreDeltaError {
				_ = delta("<tool_call>secret</tool_call>")
				return "safe-looking final", nil
			}
			return "<tool_call>secret</tool_call>", nil
		}}
		speech := &pipelineTestSpeech{}
		var events pipelineTestEvents
		err := segmentedSpeech(pipelineTestContext(t), engine, speech, "question", "", events.emit)
		if !errors.Is(err, replytext.ErrProtocol) || speech.calls.Load() != 0 {
			t.Fatalf("err=%v calls=%d", err, speech.calls.Load())
		}
		for _, r := range events.snapshot() {
			if r.phase == "response" {
				t.Fatal("unvalidated final response published")
			}
		}
	}
	// Non-streaming fake/future brains receive the same protection.
	var events pipelineTestEvents
	speech := &pipelineTestSpeech{}
	if err := segmentedSpeech(pipelineTestContext(t), savedReplySpeech("<function_calls>secret"), speech, "", "", events.emit); !errors.Is(err, replytext.ErrProtocol) || speech.calls.Load() != 0 {
		t.Fatalf("nonstreaming: err=%v calls=%d", err, speech.calls.Load())
	}
}

func TestVoiceProtocolGuardRejectsCitationJoinedMarkers(t *testing.T) {
	for _, response := range []string{
		"<tool_[1]call>secret</tool_[2]call>",
		"<tool_[1]search>secret</tool_[2]search>",
		"<funct[1]ion_calls>secret</funct[2]ion_calls>",
		"<｜DS[1]ML｜function_calls>secret",
		"<to[1]ol_", // Only the post-citation Guard.Finish can reject this delta tail.
		"<func[1]tion_",
	} {
		if err := replytext.Validate(response); err != nil {
			t.Fatalf("fixture already rejected raw: %v", err)
		}
		if err := replytext.Validate(cleanResponseText(response)); !errors.Is(err, replytext.ErrProtocol) {
			t.Fatalf("fixture is not a cleaned protocol: %q", response)
		}
		for _, streaming := range []bool{false, true} {
			for _, mode := range []string{"whole", "bytes", "no-delta", "ignored-bytes"} {
				for _, safeFinal := range []bool{false, true} {
					if mode == "no-delta" && safeFinal {
						continue
					}
					engine := &pipelineTestBrain{stream: func(_ context.Context, _ string, delta func(string) error) (string, error) {
						if mode != "no-delta" {
							parts := []string{response}
							if mode != "whole" {
								parts = nil
								for i := range len(response) {
									parts = append(parts, response[i:i+1])
								}
							}
							for _, part := range parts {
								if err := delta(part); err != nil && mode != "ignored-bytes" {
									return "", err
								}
							}
						}
						if safeFinal {
							return "safe final", nil
						}
						return response, nil
					}}
					wav := &pipelineTestSpeech{synth: func(context.Context, string, string, int) ([]int16, error) { return []int16{1}, nil }}
					fish := &streamTestSpeech{stream: func(context.Context, <-chan string, func([]int16) error) error { return nil }}
					var speech speechAPI = wav
					if streaming {
						speech = fish
					}
					var events pipelineTestEvents
					err := segmentedSpeech(pipelineTestContext(t), engine, speech, "question", "", events.emit)
					if !errors.Is(err, replytext.ErrProtocol) {
						t.Fatalf("mode=%s stream=%v safeFinal=%v response=%q: %v", mode, streaming, safeFinal, response, err)
					}
					if wav.calls.Load() != 0 || fish.calls.Load() != 0 || fish.synthCalls.Load() != 0 {
						t.Fatal("citation-joined protocol reached TTS")
					}
					if strings.Contains(err.Error(), "secret") {
						t.Fatal("protocol leaked in error")
					}
					for _, r := range events.snapshot() {
						if r.phase == "response" {
							t.Fatalf("citation-joined protocol entered response: %q", r.text)
						}
					}
				}
			}
		}
	}
}

func TestVoiceProtocolGuardFlushesLegalText(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, response := range []string{"比较 1 < 2 > 0，a <= b，c >= d。", "保留末尾 <", "保留末尾 <t", "保留引用前缀[12<", "中文[12", "合法全角符号 <｜比较"} {
			engine := &pipelineTestBrain{stream: func(_ context.Context, _ string, delta func(string) error) (string, error) {
				for i := range len(response) {
					if err := delta(response[i : i+1]); err != nil {
						return "", err
					}
				}
				return response, nil
			}}
			var spoken strings.Builder
			var speech speechAPI = &pipelineTestSpeech{synth: func(_ context.Context, text, _ string, _ int) ([]int16, error) {
				spoken.WriteString(text)
				return []int16{1}, nil
			}}
			if streaming {
				speech = &streamTestSpeech{stream: func(_ context.Context, parts <-chan string, audio func([]int16) error) error {
					for part := range parts {
						spoken.WriteString(part)
					}
					return audio([]int16{1})
				}}
			}
			var events pipelineTestEvents
			if err := segmentedSpeech(pipelineTestContext(t), engine, speech, "question", "", events.emit); err != nil {
				t.Fatalf("%q: %v", response, err)
			}
			if spoken.String() != response {
				t.Fatalf("speech=%q want=%q", spoken.String(), response)
			}
			var final string
			for _, r := range events.snapshot() {
				if r.phase == "response" && r.final {
					final = r.text
				}
			}
			if final != response {
				t.Fatalf("final=%q want=%q", final, response)
			}
		}
	}
}

func TestVoiceProtocolGuardExplicitCommandsRejectBeforeDisplay(t *testing.T) {
	for _, kind := range []string{"preview", "reference", "respeak", "replay"} {
		t.Run(kind, func(t *testing.T) {
			s, outputs := replayTestSession(t)
			s.state.Response = "previous safe response"
			const protocol = "<tool_call>secret</tool_call>"
			s.replayCache = []cachedReply{{key: ReplayTextKey(protocol), turn: 42, text: protocol, pcm: []int16{1}}}
			s.emit = func(state State) {
				if strings.Contains(state.Response, "<tool_") || strings.Contains(state.Notice, "secret") {
					t.Fatal("protocol displayed")
				}
			}
			if err := s.command(Command{Kind: kind, Text: protocol, Turn: 42}); err != nil {
				t.Fatal(err)
			}
			if len(s.jobs) != 0 || len(*outputs) != 0 || s.active {
				t.Fatal("invalid command started speech")
			}
			if s.state.Response != "previous safe response" || len(s.replayCache) != 1 {
				t.Fatal("rejection destroyed previous response/history")
			}
			if s.state.Notice != replytext.ErrProtocol.Error() {
				t.Fatalf("notice=%q", s.state.Notice)
			}
		})
	}
}

func TestVoiceProtocolGuardDoesNotFilterUserInput(t *testing.T) {
	s, _ := replayTestSession(t)
	const text = "请解释 <tool_call> 标签，不要执行。"
	if err := s.command(Command{Kind: "text", Text: text}); err != nil {
		t.Fatal(err)
	}
	if len(s.jobs) != 1 || s.state.Transcript != text {
		t.Fatal("ordinary user input was filtered")
	}
	job := <-s.jobs
	if job.kind != "text" || job.text != text {
		t.Fatal("user input changed before brain")
	}
}

func TestVoiceProtocolGuardFishReferenceIDIgnoresInactiveDraft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := fakeDependencies()
	speech := &streamTestSpeech{stream: func(_ context.Context, parts <-chan string, audio func([]int16) error) error {
		for part := range parts {
			if part != "safe preview" {
				return errors.New("unexpected speech input")
			}
		}
		return audio([]int16{1})
	}}
	d.newSpeech = func(Config) speechAPI { return speech }
	cfg := Config{TTSProvider: "fish", FishAPIKey: "test", FishReferenceID: "active-reference", FishReferenceText: "<tool_call>inactive secret draft</tool_call>"}
	jobs, out := make(chan work, 1), make(chan result, 16)
	done := make(chan struct{})
	go func() { defer close(done); worker(ctx, cfg, nil, d, jobs, out) }()
	defer func() { cancel(); <-done }()
	jobs <- work{ctx: ctx, kind: "preview", text: "safe preview"}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case r := <-out:
			if r.err != nil {
				t.Fatalf("inactive reference draft blocked synthesis: %v", r.err)
			}
			if r.phase == "generation_done" {
				if speech.calls.Load() != 1 {
					t.Fatal("preview did not use active reference")
				}
				return
			}
		case <-deadline:
			t.Fatal("preview did not complete")
		}
	}
}

func TestVoiceProtocolGuardWorkerExplicitInputs(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, kind := range []string{"preview", "reference", "respeak", "fish-reference"} {
			t.Run(kind+map[bool]string{false: "-wav", true: "-stream"}[streaming], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var tts, reference, factory atomic.Int32
				d := fakeDependencies()
				d.reference = func(string) ([]int16, error) { reference.Add(1); return []int16{1}, nil }
				d.newSpeech = func(Config) speechAPI {
					factory.Add(1)
					if streaming {
						return &streamTestSpeech{stream: func(context.Context, <-chan string, func([]int16) error) error { tts.Add(1); return nil }}
					}
					return &fakeSpeech{synthesize: func(context.Context, string, string) ([]int16, error) { tts.Add(1); return []int16{1}, nil }}
				}
				cfg := Config{MiMoAPIKey: "test"}
				w := work{ctx: ctx, kind: kind, text: "<tool_call>secret</tool_call>"}
				if kind == "fish-reference" {
					cfg.TTSProvider = "fish"
					cfg.FishAPIKey = "test"
					cfg.FishReferenceText = w.text
					w.kind = "preview"
					w.text = "safe preview"
				}
				jobs, out := make(chan work, 1), make(chan result, 16)
				done := make(chan struct{})
				go func() { defer close(done); worker(ctx, cfg, nil, d, jobs, out) }()
				defer func() { cancel(); <-done }()
				jobs <- w
				select {
				case r := <-out:
					if !errors.Is(r.err, replytext.ErrProtocol) || r.text != "" {
						t.Fatalf("unexpected result: %+v", r)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("worker did not reject protocol")
				}
				if tts.Load() != 0 || reference.Load() != 0 || factory.Load() != 0 {
					t.Fatal("protocol reached speech setup or synthesis")
				}
			})
		}
	}
}
