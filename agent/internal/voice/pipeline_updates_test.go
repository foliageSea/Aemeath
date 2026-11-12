package voice

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestResponseUpdatesBeforeMiMoBoundary(t *testing.T) {
	ctx := pipelineTestContext(t)
	release := make(chan struct{})
	pushed := make(chan struct{})
	engine := &pipelineTestBrain{stream: func(c context.Context, _ string, delta func(string) error) (string, error) {
		if err := delta("还没到合成边界"); err != nil {
			return "", err
		}
		close(pushed)
		if err := pipelineTestBarrier(c, release); err != nil {
			return "", err
		}
		if err := delta("，但文字应当已经显示。"); err != nil {
			return "", err
		}
		return "还没到合成边界，但文字应当已经显示。", nil
	}}
	speech := &pipelineTestSpeech{synth: func(context.Context, string, string, int) ([]int16, error) { return []int16{1}, nil }}
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, engine, speech, "问题", "", events.emit) }()
	pipelineTestWait(t, pushed, "text update without speech boundary")
	if speech.calls.Load() != 0 {
		t.Fatal("MiMo submitted an incomplete short fragment")
	}
	got := events.snapshot()
	if len(got) != 1 || got[0].phase != "response" || got[0].text != "还没到合成边界" {
		t.Fatalf("text withheld: %+v", got)
	}
	close(release)
	if err := pipelineTestResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestResponseUpdatesAfterMiMoSegmentBudget(t *testing.T) {
	ctx := pipelineTestContext(t)
	release := make(chan struct{})
	pushed := make(chan struct{})
	text := strings.Repeat("这是足够长且完整的一句话，我们需要保留全部的文字内容。", 40)
	engine := &pipelineTestBrain{stream: func(c context.Context, _ string, delta func(string) error) (string, error) {
		for _, r := range text {
			if err := delta(string(r)); err != nil {
				return "", err
			}
		}
		close(pushed)
		if err := pipelineTestBarrier(c, release); err != nil {
			return "", err
		}
		return text, nil
	}}
	speech := &pipelineTestSpeech{synth: func(context.Context, string, string, int) ([]int16, error) { return []int16{1}, nil }}
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, engine, speech, "问题", "", events.emit) }()
	pipelineTestWait(t, pushed, "all text before EOF")
	var displayed string
	for _, r := range events.snapshot() {
		if r.phase == "response" {
			displayed = r.text
		}
	}
	if displayed != text {
		t.Fatal("UI stopped updating after seven MiMo segments")
	}
	close(release)
	if err := pipelineTestResult(t, done); err != nil {
		t.Fatal(err)
	}
	if speech.calls.Load() != 8 {
		t.Fatalf("MiMo request budget changed: %d", speech.calls.Load())
	}
}

func TestFishTextUpdatesWhileProviderStopsReading(t *testing.T) {
	ctx := pipelineTestContext(t)
	release := make(chan struct{})
	generated := make(chan struct{})
	text := strings.Repeat("hello world ", 100)
	engine := &pipelineTestBrain{stream: func(c context.Context, _ string, delta func(string) error) (string, error) {
		for i := range len(text) {
			if err := delta(text[i : i+1]); err != nil {
				return "", err
			}
		}
		close(generated)
		return text, nil
	}}
	spoken := make(chan string, 1)
	speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
		if err := pipelineTestBarrier(c, release); err != nil {
			return err
		}
		var b strings.Builder
		for part := range in {
			b.WriteString(part)
		}
		spoken <- b.String()
		return audio([]int16{1})
	}}
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, engine, speech, "问题", "", events.emit) }()
	pipelineTestWait(t, generated, "LLM text independent of Fish input backpressure")
	var displayed string
	for _, r := range events.snapshot() {
		if r.phase == "response" {
			displayed = r.text
		}
	}
	if displayed != strings.TrimSpace(text) {
		t.Fatal("slow Fish blocked UI text")
	}
	close(release)
	if err := pipelineTestResult(t, done); err != nil {
		t.Fatal(err)
	}
	if got := <-spoken; got != text {
		t.Fatalf("Fish lost whitespace or reordered text: %q", got)
	}
}

func TestFishContinuesSubmittingBeyondEightBeforeLLMEOF(t *testing.T) {
	ctx := pipelineTestContext(t)
	received := make(chan struct{}, 1)
	finished := make(chan struct{})
	clause := "短句可以及时提交。"
	engine := &pipelineTestBrain{stream: func(c context.Context, _ string, delta func(string) error) (string, error) {
		for range 12 {
			if err := delta(clause); err != nil {
				return "", err
			}
			if err := pipelineTestBarrier(c, received); err != nil {
				return "", err
			}
		}
		close(finished)
		return strings.Repeat(clause, 12), nil
	}}
	var spoken []string
	speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
		for part := range in {
			spoken = append(spoken, part)
			select {
			case received <- struct{}{}:
			case <-c.Done():
				return c.Err()
			}
		}
		return audio([]int16{1})
	}}
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, engine, speech, "问题", "", events.emit) }()
	pipelineTestWait(t, finished, "twelve short Fish submissions before EOF")
	if err := pipelineTestResult(t, done); err != nil {
		t.Fatal(err)
	}
	if len(spoken) != 12 || strings.Join(spoken, "") != strings.Repeat(clause, 12) {
		t.Fatalf("Fish submissions: %#v", spoken)
	}
	if speech.calls.Load() != 1 {
		t.Fatal("Fish opened multiple sessions")
	}
}

func TestFishTextBatchTimeoutAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(pipelineTestContext(t))
	in := make(chan string)
	out := make(chan string)
	done := make(chan struct{})
	go func() { defer close(done); forwardFishText(ctx, "尚无标点", in, out, 5*time.Millisecond) }()
	select {
	case got := <-out:
		if got != "尚无标点" {
			t.Fatalf("unexpected flush: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("text without punctuation waited indefinitely")
	}
	cancel()
	pipelineTestWait(t, done, "Fish text batcher cancellation")
	if _, ok := <-out; ok {
		t.Fatal("batcher did not close output")
	}
}

func TestSpeechTextStreamUTF8AndLimit(t *testing.T) {
	text := "中文👩‍🚀 hello world "
	var stream speechTextStream
	var got strings.Builder
	for i := range len(text) {
		part, err := stream.Push(text[i : i+1])
		if err != nil || !utf8.ValidString(part) {
			t.Fatalf("invalid partial text: %q %v", part, err)
		}
		got.WriteString(part)
	}
	if stream.Finish() != nil || got.String() != text {
		t.Fatal("UTF-8 text not preserved")
	}
	for _, bad := range []string{"\xff", "\xe4"} {
		var s speechTextStream
		_, err := s.Push(bad)
		if err == nil {
			err = s.Finish()
		}
		if err == nil {
			t.Fatalf("malformed text accepted: %q", bad)
		}
	}
	var s speechTextStream
	if _, err := s.Push(strings.Repeat("a", maxSpeechTextBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push("a"); err == nil || s.Finish() == nil {
		t.Fatal("text limit not terminal")
	}
}
