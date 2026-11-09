package voice

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type streamTestSpeech struct {
	fakeSpeech
	stream     func(context.Context, <-chan string, func([]int16) error) error
	calls      atomic.Int32
	synthCalls atomic.Int32
}

func (s *streamTestSpeech) Stream(c context.Context, text <-chan string, audio func([]int16) error) error {
	s.calls.Add(1)
	return s.stream(c, text, audio)
}
func (s *streamTestSpeech) Synthesize(context.Context, string, string) ([]int16, error) {
	s.synthCalls.Add(1)
	return nil, errors.New("streaming speech used non-streaming fallback")
}

func TestFishStreamPipelineAudioBeforeLLMEOFAndFishFinish(t *testing.T) {
	ctx := pipelineTestContext(t)
	releaseLLM := make(chan struct{})
	releaseFish := make(chan struct{})
	llmDone := make(chan struct{})
	fishTextEOF := make(chan struct{})
	firstAudio := make(chan struct{})
	first := "你好，我是爱弥斯。"
	tail := "这是流式回复的剩余内容。"
	engine := &pipelineTestBrain{stream: func(c context.Context, _ string, delta func(string) error) (string, error) {
		defer close(llmDone)
		if e := delta(first); e != nil {
			return "", e
		}
		if e := pipelineTestBarrier(c, releaseLLM); e != nil {
			return "", e
		}
		if e := delta(tail); e != nil {
			return "", e
		}
		return first + tail, nil
	}}
	texts := make(chan string, 1)
	speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
		initial, ok := <-in
		if !ok {
			return errors.New("text closed before first clause")
		}
		pcm := make([]int16, 650)
		for i := range pcm {
			pcm[i] = int16(i)
		}
		if e := audio(pcm); e != nil {
			return e
		}
		for i := range pcm {
			pcm[i] = -1
		}
		close(firstAudio)
		var all strings.Builder
		all.WriteString(initial)
		for {
			select {
			case part, more := <-in:
				if !more {
					close(fishTextEOF)
					texts <- all.String()
					return pipelineTestBarrier(c, releaseFish)
				}
				all.WriteString(part)
			case <-c.Done():
				return c.Err()
			}
		}
	}}
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- segmentedSpeech(ctx, engine, speech, "question", "ref", events.emit) }()
	pipelineTestWait(t, firstAudio, "first PCM before LLM completion")
	select {
	case <-llmDone:
		t.Fatal("LLM already completed before first audio")
	default:
	}
	for _, r := range events.snapshot() {
		if r.phase == "audio_stream_end" || r.phase == "generation_done" {
			t.Fatal("temporary empty stream completed")
		}
	}
	close(releaseLLM)
	pipelineTestWait(t, fishTextEOF, "Fish text EOF")
	select {
	case <-done:
		t.Fatal("pipeline completed before Fish finish")
	default:
	}
	close(releaseFish)
	if e := pipelineTestResult(t, done); e != nil {
		t.Fatal(e)
	}
	if text := <-texts; text != first+tail {
		t.Fatalf("text order changed: %q", text)
	}
	var chunks []int
	starts, ends, generated := 0, 0, 0
	sample := 0
	for _, r := range events.snapshot() {
		switch r.phase {
		case "audio_stream_start":
			starts++
		case "audio_stream_chunk":
			chunks = append(chunks, len(r.pcm))
			for _, v := range r.pcm {
				if v != int16(sample) {
					t.Fatalf("PCM not owned or out of order at %d: %d", sample, v)
				}
				sample++
			}
		case "audio_stream_end":
			ends++
			if r.samples != 650 {
				t.Fatalf("wrong total %d", r.samples)
			}
		case "generation_done":
			generated++
		}
	}
	if !reflect.DeepEqual(chunks, []int{320, 320, 10}) || starts != 1 || ends != 1 || generated != 1 || speech.calls.Load() != 1 || speech.synthCalls.Load() != 0 {
		t.Fatalf("bad lifecycle chunks=%v starts=%d ends=%d done=%d", chunks, starts, ends, generated)
	}
}

func TestFishStreamPipelineFailureCancelsAndJoinsProducer(t *testing.T) {
	for _, kind := range []string{"provider_error", "consumer_cancel", "audio_limit", "empty_audio"} {
		t.Run(kind, func(t *testing.T) {
			ctx := pipelineTestContext(t)
			producerDone := make(chan struct{})
			streamDone := make(chan struct{})
			engine := &pipelineTestBrain{stream: func(c context.Context, _ string, delta func(string) error) (string, error) {
				defer close(producerDone)
				if e := delta("你好，这是第一句话。"); e != nil {
					return "", e
				}
				<-c.Done()
				return "", c.Err()
			}}
			speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
				defer close(streamDone)
				select {
				case <-in:
				case <-c.Done():
					return c.Err()
				}
				switch kind {
				case "provider_error":
					return errors.New("Fish connection interrupted")
				case "audio_limit":
					return audio(make([]int16, maxPlaybackSamples+1))
				case "empty_audio":
					return nil
				default:
					return audio(make([]int16, 320))
				}
			}}
			var events pipelineTestEvents
			done := make(chan error, 1)
			go func() {
				done <- segmentedSpeech(ctx, engine, speech, "question", "", func(c context.Context, r result) bool {
					if kind == "consumer_cancel" && r.phase == "audio_stream_chunk" {
						return false
					}
					return events.emit(c, r)
				})
			}()
			if e := pipelineTestResult(t, done); e == nil {
				t.Fatal("stream failure ignored")
			}
			pipelineTestClosed(t, producerDone, "LLM producer")
			pipelineTestClosed(t, streamDone, "Fish stream")
			if speech.calls.Load() != 1 || engine.streamCalls.Load() != 1 || engine.replyCalls.Load() != 0 {
				t.Fatal("failed request replayed")
			}
			for _, r := range events.snapshot() {
				if r.phase == "audio_stream_end" || r.phase == "generation_done" {
					t.Fatal("failed stream emitted successful EOF")
				}
			}
		})
	}
}

func TestFishStreamPipelineCumulativeAudioLimit(t *testing.T) {
	ctx := pipelineTestContext(t)
	parts := make(chan string, 1)
	parts <- "测试总音频上限。"
	close(parts)
	speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
		<-in
		half := make([]int16, maxPlaybackSamples/2)
		if e := audio(half); e != nil {
			return e
		}
		if e := audio(half); e != nil {
			return e
		}
		return audio([]int16{1})
	}}
	var emitted int
	var ended bool
	err := streamSpeech(ctx, speech, parts, func(c context.Context, r result) bool {
		if r.phase == "audio_stream_chunk" {
			if len(r.pcm) > 320 {
				t.Error("callback result exceeds frame bound")
			}
			emitted += len(r.pcm)
		}
		if r.phase == "audio_stream_end" || r.phase == "generation_done" {
			ended = true
		}
		return true
	})
	if err == nil || emitted != maxPlaybackSamples || ended {
		t.Fatalf("cumulative cap: err=%v samples=%d ended=%v", err, emitted, ended)
	}
}

func TestFishStreamPipelineWaitsForFirstPCMAndParentCancelJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	joined := make(chan struct{})
	speech := &streamTestSpeech{stream: func(c context.Context, in <-chan string, audio func([]int16) error) error {
		defer close(joined)
		<-in
		close(started)
		if e := audio(nil); e != nil {
			return e
		}
		<-c.Done()
		return c.Err()
	}}
	parts := make(chan string, 1)
	parts <- "你好。"
	var events pipelineTestEvents
	done := make(chan error, 1)
	go func() { done <- streamSpeech(ctx, speech, parts, events.emit) }()
	pipelineTestWait(t, started, "Fish session start")
	for _, r := range events.snapshot() {
		if strings.HasPrefix(r.phase, "audio_stream_") {
			t.Fatal("playback started before PCM")
		}
	}
	cancel()
	if e := pipelineTestResult(t, done); e == nil {
		t.Fatal("cancel ignored")
	}
	pipelineTestClosed(t, joined, "Fish reader/sender")
}
