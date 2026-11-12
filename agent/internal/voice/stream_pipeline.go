package voice

import (
	"context"
	"errors"
	"time"
)

type streamingSpeech interface {
	Stream(context.Context, <-chan string, func([]int16) error) error
}

// Use lightweight text batching in a single Fish session, independent of MiMo.
// PCM callbacks are split into owned frame-sized results to bound backpressure.
func streamSpeech(parent context.Context, speech streamingSpeech, parts <-chan string, emit func(context.Context, result) bool) error {
	var first string
	var ok bool
	select {
	case <-parent.Done():
		return context.Cause(parent)
	case first, ok = <-parts:
	}
	if !ok {
		return &pipelineFailure{stage: "synthesizing", err: errors.New("no speakable response")}
	}
	ctx, cancel := context.WithTimeout(parent, synthesisBudget)
	defer cancel()
	input := make(chan string, streamTextQueueDepth)
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		forwardFishText(ctx, first, parts, input, fishTextMaxWait)
	}()
	defer func() { cancel(); <-forwarded }()
	start := time.Now()
	total := 0
	started := false
	if !emit(ctx, result{phase: "synthesizing"}) {
		return context.Canceled
	}
	err := speech.Stream(ctx, input, func(pcm []int16) error {
		if len(pcm) == 0 {
			return nil
		}
		if len(pcm) > maxPlaybackSamples-total {
			return errors.New("streaming audio exceeds 120 seconds")
		}
		if !started {
			if !emit(ctx, result{phase: "audio_stream_start"}) {
				return context.Canceled
			}
			started = true
		}
		total += len(pcm)
		for len(pcm) > 0 {
			n := min(frameSamples, len(pcm))
			chunk := append([]int16(nil), pcm[:n]...)
			pcm = pcm[n:]
			if !emit(ctx, result{phase: "audio_stream_chunk", pcm: chunk, millis: time.Since(start).Milliseconds()}) {
				return context.Canceled
			}
		}
		return nil
	})
	if err != nil {
		return &pipelineFailure{stage: "synthesizing", err: err, millis: max(1, time.Since(start).Milliseconds())}
	}
	if total == 0 {
		return &pipelineFailure{stage: "synthesizing", err: errors.New("Fish returned no audio"), millis: time.Since(start).Milliseconds()}
	}
	if !emit(ctx, result{phase: "audio_stream_end", samples: total, millis: max(1, time.Since(start).Milliseconds())}) {
		return context.Canceled
	}
	if !emit(ctx, result{phase: "generation_done"}) {
		return context.Canceled
	}
	return nil
}
