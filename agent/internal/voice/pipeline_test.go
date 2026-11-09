package voice

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pipelineTestBrain struct {
	stream      func(context.Context, string, func(string) error) (string, error)
	streamCalls atomic.Int32
	replyCalls  atomic.Int32
}

func (b *pipelineTestBrain) Reply(context.Context, string) (string, error) {
	b.replyCalls.Add(1)
	return "", errors.New("unexpected non-streaming fallback")
}
func (*pipelineTestBrain) Clear() {}
func (b *pipelineTestBrain) ReplyStream(ctx context.Context, text string, delta func(string) error) (string, error) {
	b.streamCalls.Add(1)
	return b.stream(ctx, text, delta)
}

type pipelineTestSpeech struct {
	synth func(context.Context, string, string, int) ([]int16, error)
	calls atomic.Int32
}

func (*pipelineTestSpeech) Recognize(context.Context, []int16) (string, error) {
	return "", errors.New("unexpected ASR")
}
func (s *pipelineTestSpeech) Synthesize(ctx context.Context, text, reference string) ([]int16, error) {
	return s.synth(ctx, text, reference, int(s.calls.Add(1)))
}

type pipelineTestEvents struct {
	mu     sync.Mutex
	events []result
}

func (e *pipelineTestEvents) emit(ctx context.Context, r result) bool {
	if ctx.Err() != nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, r)
	return true
}
func (e *pipelineTestEvents) snapshot() []result {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]result(nil), e.events...)
}
func pipelineTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}
func pipelineTestWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
func pipelineTestResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not return promptly")
		return nil
	}
}
func pipelineTestClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatalf("pipeline returned without joining %s", what)
	}
}
func pipelineTestBarrier(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func pipelineTestFailure(t *testing.T, err error, stage string, cause error) {
	t.Helper()
	var failure *pipelineFailure
	if !errors.As(err, &failure) || failure.stage != stage {
		t.Fatalf("error=%v; want pipelineFailure stage %s", err, stage)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("error=%v; want cause %v", err, cause)
	}
}

func TestSegmentedSpeechFirstAudioBeforeLLMEOFAndOrderedTail(t *testing.T) {
	ctx := pipelineTestContext(t)
	first := "这是第一段已经完整可以开始朗读的内容。"
	second := strings.Repeat("乙", 60) + "。"
	tail := "这是最后一段。"
	full := first + second + tail
	producerBlocked := make(chan struct{})
	finishLLM := make(chan struct{})
	firstAudio := make(chan struct{})
	producerExited := make(chan struct{})
	brain := &pipelineTestBrain{stream: func(ctx context.Context, text string, delta func(string) error) (string, error) {
		defer close(producerExited)
		if text != "提问" {
			t.Errorf("input=%q", text)
		}
		if err := delta(first); err != nil {
			return "", err
		}
		close(producerBlocked)
		if err := pipelineTestBarrier(ctx, finishLLM); err != nil {
			return "", err
		}
		if err := delta(second); err != nil {
			return "", err
		}
		if err := delta(tail); err != nil {
			return "", err
		}
		return full, nil
	}}
	var spoken []string
	speech := &pipelineTestSpeech{synth: func(ctx context.Context, text, reference string, index int) ([]int16, error) {
		if reference != "reference.wav" {
			t.Errorf("reference=%q", reference)
		}
		spoken = append(spoken, text)
		return []int16{int16(index), int16(index)}, nil
	}}
	var events pipelineTestEvents
	var audioOnce sync.Once
	emit := func(ctx context.Context, r result) bool {
		if !events.emit(ctx, r) {
			return false
		}
		if r.phase == "audio_segment" {
			audioOnce.Do(func() {
				if r.final {
					t.Error("first segment marked final while LLM still blocked")
				}
				close(firstAudio)
			})
		}
		return true
	}
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, brain, speech, "提问", "reference.wav", emit) }()
	pipelineTestWait(t, producerBlocked, "LLM barrier")
	pipelineTestWait(t, firstAudio, "first audio while LLM is blocked")
	select {
	case <-producerExited:
		t.Fatal("LLM completed before first audio barrier")
	default:
	}
	select {
	case err := <-done:
		t.Fatalf("pipeline finished before LLM EOF: %v", err)
	default:
	}
	close(finishLLM)
	if err := pipelineTestResult(t, done); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spoken, []string{first, second, tail}) {
		t.Fatalf("spoken order=%q", spoken)
	}
	var audio []int16
	var responses []string
	var completed int
	for _, r := range events.snapshot() {
		switch r.phase {
		case "audio_segment":
			audio = append(audio, r.pcm[0])
		case "response":
			responses = append(responses, r.text)
		case "generation_done":
			completed++
		}
	}
	if !reflect.DeepEqual(audio, []int16{1, 2, 3}) {
		t.Fatalf("audio order=%v", audio)
	}
	if len(responses) == 0 || responses[len(responses)-1] != full || completed != 1 {
		t.Fatalf("responses=%q completed=%d", responses, completed)
	}
	if brain.streamCalls.Load() != 1 || brain.replyCalls.Load() != 0 {
		t.Fatal("stream was retried or downgraded")
	}
	pipelineTestClosed(t, producerExited, "LLM producer")
}

func TestSegmentedSpeechPartialStreamErrorDoesNotRetry(t *testing.T) {
	ctx := pipelineTestContext(t)
	cause := errors.New("LLM broke after partial reply")
	audioSent := make(chan struct{})
	producerExited := make(chan struct{})
	brain := &pipelineTestBrain{stream: func(ctx context.Context, _ string, delta func(string) error) (string, error) {
		defer close(producerExited)
		if err := delta("已经生成可以播放的第一句话。"); err != nil {
			return "", err
		}
		if err := pipelineTestBarrier(ctx, audioSent); err != nil {
			return "", err
		}
		return "已经生成可以播放的第一句话。", cause
	}}
	speech := &pipelineTestSpeech{synth: func(context.Context, string, string, int) ([]int16, error) { return []int16{1}, nil }}
	var events pipelineTestEvents
	emit := func(ctx context.Context, r result) bool {
		if !events.emit(ctx, r) {
			return false
		}
		if r.phase == "audio_segment" {
			close(audioSent)
		}
		return true
	}
	err := segmentedSpeech(ctx, brain, speech, "提问", "reference.wav", emit)
	pipelineTestFailure(t, err, "thinking", cause)
	if brain.streamCalls.Load() != 1 || brain.replyCalls.Load() != 0 || speech.calls.Load() != 1 {
		t.Fatalf("stream=%d fallback=%d TTS=%d", brain.streamCalls.Load(), brain.replyCalls.Load(), speech.calls.Load())
	}
	for _, r := range events.snapshot() {
		if r.phase == "generation_done" || r.final {
			t.Fatal("partial failed reply announced completion")
		}
	}
	pipelineTestClosed(t, producerExited, "failed LLM producer")
}

func TestSegmentedSpeechTTSFailureCancelsAndJoinsProducer(t *testing.T) {
	ctx := pipelineTestContext(t)
	cause := errors.New("TTS failed")
	producerBlocked := make(chan struct{})
	producerExited := make(chan struct{})
	brain := &pipelineTestBrain{stream: func(ctx context.Context, _ string, delta func(string) error) (string, error) {
		defer close(producerExited)
		if err := delta("第一句已经足够触发语音合成。"); err != nil {
			return "", err
		}
		close(producerBlocked)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	speech := &pipelineTestSpeech{synth: func(ctx context.Context, _ string, _ string, _ int) ([]int16, error) {
		if err := pipelineTestBarrier(ctx, producerBlocked); err != nil {
			return nil, err
		}
		return nil, cause
	}}
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, brain, speech, "提问", "ref", events.emit) }()
	pipelineTestFailure(t, pipelineTestResult(t, done), "synthesizing", cause)
	pipelineTestClosed(t, producerExited, "producer after TTS failure")
	if speech.calls.Load() != 1 || brain.streamCalls.Load() != 1 {
		t.Fatal("failed synthesis replayed")
	}
	for _, r := range events.snapshot() {
		if r.phase == "audio_segment" || r.phase == "generation_done" {
			t.Fatalf("unexpected event %s", r.phase)
		}
	}
}

func TestSegmentedSpeechParentCancelJoinsLLMAndTTS(t *testing.T) {
	ctx, cancel := context.WithCancel(pipelineTestContext(t))
	defer cancel()
	ttsEntered := make(chan struct{})
	ttsExited := make(chan struct{})
	producerExited := make(chan struct{})
	brain := &pipelineTestBrain{stream: func(ctx context.Context, _ string, delta func(string) error) (string, error) {
		defer close(producerExited)
		if err := delta("第一句会进入正在等待取消的合成请求。"); err != nil {
			return "", err
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	speech := &pipelineTestSpeech{synth: func(ctx context.Context, _ string, _ string, _ int) ([]int16, error) {
		defer close(ttsExited)
		close(ttsEntered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, brain, speech, "提问", "ref", events.emit) }()
	pipelineTestWait(t, ttsEntered, "TTS request")
	cancel()
	if err := pipelineTestResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	pipelineTestClosed(t, producerExited, "LLM after parent cancellation")
	pipelineTestClosed(t, ttsExited, "TTS after parent cancellation")
	for _, r := range events.snapshot() {
		if r.phase == "audio_segment" || r.phase == "generation_done" {
			t.Fatalf("event after cancellation=%s", r.phase)
		}
	}
}

func TestSegmentedSpeechTotalPCMCap(t *testing.T) {
	if maxPlaybackSamples != 16000*120 {
		t.Fatalf("playback budget=%d; want 120 seconds at16k", maxPlaybackSamples)
	}
	for _, over := range []bool{false, true} {
		name := "exact120seconds"
		if over {
			name = "rejectCumulativeOverflow"
		}
		t.Run(name, func(t *testing.T) {
			ctx := pipelineTestContext(t)
			first := "这是第一句话。"
			second := strings.Repeat("乙", 60) + "。"
			tail := "最后一段。"
			full := first + second
			if over {
				full += tail
			}
			brain := &pipelineTestBrain{stream: func(ctx context.Context, _ string, delta func(string) error) (string, error) {
				if err := delta(full); err != nil {
					return "", err
				}
				return full, nil
			}}
			speech := &pipelineTestSpeech{synth: func(_ context.Context, _ string, _ string, index int) ([]int16, error) {
				if index == 1 {
					return make([]int16, maxPlaybackSamples-1), nil
				}
				return []int16{1}, nil
			}}
			var events pipelineTestEvents
			err := segmentedSpeech(ctx, brain, speech, "提问", "ref", events.emit)
			if over {
				pipelineTestFailure(t, err, "synthesizing", nil)
			} else if err != nil {
				t.Fatal(err)
			}
			accepted, completed := 0, 0
			for _, r := range events.snapshot() {
				if r.phase == "audio_segment" {
					accepted += len(r.pcm)
				}
				if r.phase == "generation_done" {
					completed++
				}
			}
			if accepted != maxPlaybackSamples {
				t.Fatalf("accepted=%d want=%d", accepted, maxPlaybackSamples)
			}
			if (!over && completed != 1) || (over && completed != 0) {
				t.Fatalf("completed=%d overflow=%v", completed, over)
			}
			wantCalls := int32(2)
			if over {
				wantCalls = 3
			}
			if speech.calls.Load() != wantCalls {
				t.Fatalf("TTS calls=%d want=%d", speech.calls.Load(), wantCalls)
			}
		})
	}
}

func TestSegmentedSpeechAtMostEightTTSCallsPreservesText(t *testing.T) {
	ctx := pipelineTestContext(t)
	full := strings.Repeat("这是一段用于验证语音调用上限和文本顺序的完整内容。", 90)
	brain := &pipelineTestBrain{stream: func(ctx context.Context, _ string, delta func(string) error) (string, error) {
		for _, r := range full {
			if err := delta(string(r)); err != nil {
				return "", err
			}
		}
		return full, nil
	}}
	var spoken strings.Builder
	speech := &pipelineTestSpeech{synth: func(_ context.Context, text, _ string, index int) ([]int16, error) {
		spoken.WriteString(text)
		return []int16{int16(index)}, nil
	}}
	var events pipelineTestEvents
	if err := segmentedSpeech(ctx, brain, speech, "提问", "ref", events.emit); err != nil {
		t.Fatal(err)
	}
	if speech.calls.Load() != 8 {
		t.Fatalf("long reply TTS calls=%d want8", speech.calls.Load())
	}
	if spoken.String() != full {
		t.Fatal("segment limit lost, duplicated, or reordered text")
	}
	var audio []int16
	for _, r := range events.snapshot() {
		if r.phase == "audio_segment" {
			audio = append(audio, r.pcm[0])
		}
	}
	if !reflect.DeepEqual(audio, []int16{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("audio order=%v", audio)
	}
}

func TestSegmentedSpeechRejectedAudioEmitJoinsProducer(t *testing.T) {
	ctx := pipelineTestContext(t)
	producerExited := make(chan struct{})
	brain := &pipelineTestBrain{stream: func(ctx context.Context, _ string, delta func(string) error) (string, error) {
		defer close(producerExited)
		if err := delta("这句语音会被下游主动拒绝。"); err != nil {
			return "", err
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	speech := &pipelineTestSpeech{synth: func(context.Context, string, string, int) ([]int16, error) { return []int16{1}, nil }}
	emit := func(_ context.Context, r result) bool { return r.phase != "audio_segment" }
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, brain, speech, "提问", "ref", emit) }()
	if err := pipelineTestResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("emit rejection=%v", err)
	}
	pipelineTestClosed(t, producerExited, "producer after downstream rejection")
}
