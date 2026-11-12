package voice

import (
	"context"
	"testing"
	"time"
)

func TestWorkerTextMailboxBypassesBlockedAudioResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(pipelineTestContext(t), 2*time.Second)
	defer cancel()
	audioAttempted := make(chan struct{})
	generated := make(chan struct{})
	engine := &pipelineTestBrain{stream: func(c context.Context, _ string, delta func(string) error) (string, error) {
		if err := delta("第一句。"); err != nil {
			return "", err
		}
		if err := pipelineTestBarrier(c, audioAttempted); err != nil {
			return "", err
		}
		if err := delta("后续正文即使音频堵塞也应更新"); err != nil {
			return "", err
		}
		close(generated)
		<-c.Done()
		return "", c.Err()
	}}
	speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
		select {
		case <-in:
		case <-c.Done():
			return c.Err()
		}
		close(audioAttempted)
		return audio([]int16{1})
	}}
	dep := fakeDependencies()
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { return engine, nil }
	dep.newSpeech = func(Config) speechAPI { return speech }
	jobs := make(chan work, 1)
	out := make(chan result) // the ordered audio result stream is deliberately blocked
	responses := make(chan result, 1)
	done := make(chan struct{})
	cfg := fishStreamTestConfig()
	cfg.LLMModel = "test"
	go func() { defer close(done); workerWithResponses(ctx, cfg, nil, dep, jobs, out, responses) }()
	jobs <- work{ctx: ctx, kind: "text", text: "问题", turn: 4, generation: 2}
	select {
	case r := <-out:
		if r.phase != "thinking" {
			t.Fatalf("unexpected first event: %+v", r)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Do not consume synthesizing/start/chunk. Only provisional text may advance.
	// The synthesis phase precedes the provider call, so release it first.
	select {
	case r := <-out:
		if r.phase != "synthesizing" {
			t.Fatalf("unexpected synthesis event: %+v", r)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	pipelineTestWait(t, generated, "text update while ordered audio channel is blocked")
	r := <-responses
	if r.text != "第一句。后续正文即使音频堵塞也应更新" || r.turn != 4 || r.generation != 2 || r.final {
		t.Fatalf("wrong text snapshot: %+v", r)
	}
	cancel()
	pipelineTestWait(t, done, "worker cancellation with blocked audio")
}

func TestLatestResponseMailboxAndFinalOrdering(t *testing.T) {
	s := recordTestSession(t)
	s.responses = make(chan result, 1)
	s.active = true
	for _, text := range []string{"短", "短文", "短文字"} {
		publishLatestResponse(s.responses, result{phase: "response", text: text, turn: s.state.Turn, generation: s.generation})
	}
	if len(s.responses) != 1 {
		t.Fatal("provisional snapshots grew without bound")
	}
	if err := s.flushResponse(); err != nil {
		t.Fatal(err)
	}
	if s.state.Response != "短文字" {
		t.Fatalf("not latest snapshot: %q", s.state.Response)
	}
	if err := s.result(result{phase: "response", final: true, text: "完整正文", turn: s.state.Turn, generation: s.generation}); err != nil {
		t.Fatal(err)
	}
	if err := s.flushResponse(); err != nil || s.state.Response != "完整正文" {
		t.Fatal("partial text regressed final response")
	}
	// Existing turn/generation gates apply equally to the new mailbox.
	publishLatestResponse(s.responses, result{phase: "response", text: "旧轮次", turn: s.state.Turn + 1, generation: s.generation})
	if err := s.flushResponse(); err != nil || s.state.Response != "完整正文" {
		t.Fatal("stale text overwrote current response")
	}
}
