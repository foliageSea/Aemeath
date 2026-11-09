package voice

import (
	"context"
	"errors"
	"time"
)

type streamingSpeech interface {
	Stream(context.Context, <-chan string, func([]int16) error) error
}

// Forward the first sentence immediately; all later text shares one Fish session.
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
	input := make(chan string, maxSpeechSegments)
	input <- first
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		defer close(input)
		for {
			select {
			case <-ctx.Done():
				return
			case part, more := <-parts:
				if !more {
					return
				}
				select {
				case input <- part:
				case <-ctx.Done():
					return
				}
			}
		}
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
