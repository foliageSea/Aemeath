package voice

import (
	"context"
	"errors"
	"strings"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/hamcp"
)

type streamingBrain interface {
	ReplyStream(context.Context, string, func(string) error) (string, error)
}
type pipelineFailure struct {
	stage  string
	err    error
	millis int64
}

func (e *pipelineFailure) Error() string { return e.err.Error() }
func (e *pipelineFailure) Unwrap() error { return e.err }

// LLM text updates are independent of TTS submission boundaries. MiMo uses
// bounded sentence groups; streaming providers receive validated text deltas
// through their own lightweight batching policy. PCM is capped per reply.
func segmentedSpeech(parent context.Context, engine brainAPI, speech speechAPI, text, reference string, emit func(context.Context, result) bool) error {
	// Tool events use the same bounded, cancellation-aware channel as the other
	// worker results. They never enter the sentence splitter or speech input.
	ctx, cancel := context.WithCancelCause(parent)
	ctx = brain.WithToolObserver(ctx, func(call brain.ToolCall) {
		emit(ctx, result{toolCall: &call})
	})
	ctx = brain.WithHomeAssistantObserver(ctx, func(status hamcp.Status) {
		emit(ctx, result{homeAssistant: &status})
	})
	defer cancel(nil)
	agentCtx, stopAgent := context.WithTimeout(ctx, agentBudget)
	defer stopAgent()
	stream, streaming := speech.(streamingSpeech)
	queueSize := maxSpeechSegments
	if streaming {
		// Every nonempty delta consumes at least one of the response byte budget.
		// This accommodates the entire bounded reply even if Fish stops reading,
		// without blocking LLM text/UI updates on audio backpressure.
		queueSize = maxSpeechTextBytes
	}
	parts := make(chan string, queueSize)
	done := make(chan struct{})
	var replyErr error
	go func() {
		defer close(done)
		defer close(parts)
		start := time.Now()
		var splitter sentenceSegmenter
		var validated speechTextStream
		var speechText speechTextFilter
		var full strings.Builder
		var visible strings.Builder
		send := func(chunks []string) error {
			for _, chunk := range chunks {
				if chunk == "" || (!streaming && strings.TrimSpace(chunk) == "") {
					continue
				}
				select {
				case parts <- chunk:
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			}
			return nil
		}
		push := func(clean string) error {
			clean, err := validated.Push(clean)
			if err != nil {
				return err
			}
			if clean == "" {
				return nil
			}
			visible.WriteString(clean)
			if !emit(ctx, result{phase: "response", text: strings.TrimSpace(visible.String()), millis: time.Since(start).Milliseconds()}) {
				return context.Canceled
			}
			if streaming {
				return send([]string{clean})
			}
			chunks, err := splitter.Push(clean)
			if err != nil {
				return err
			}
			return send(chunks)
		}
		delta := func(s string) error {
			if err := agentCtx.Err(); err != nil {
				return err
			}
			if full.Len()+len(s) > maxTextBytes {
				return errors.New("agent response too long")
			}
			full.WriteString(s)
			return push(speechText.Push(s))
		}
		var response string
		var err error
		if stream, ok := engine.(streamingBrain); ok {
			response, err = stream.ReplyStream(agentCtx, text, delta)
		} else {
			response, err = engine.Reply(agentCtx, text)
			if err == nil {
				err = delta(response)
			}
		}
		if err == nil {
			if strings.TrimSpace(response) == "" || len(response) > maxTextBytes {
				err = errors.New("empty or oversized agent response")
			} else {
				err = push(speechText.Finish())
				if err == nil {
					err = validated.Finish()
				}
				if err == nil && !streaming {
					var tail []string
					tail, err = splitter.Finish()
					if err == nil {
						err = send(tail)
					}
				}
				if err == nil && !emit(ctx, result{phase: "response", final: true, text: cleanResponseText(response), millis: time.Since(start).Milliseconds()}) {
					err = context.Canceled
				}
			}
		}
		if err != nil {
			replyErr = &pipelineFailure{stage: "thinking", err: err, millis: time.Since(start).Milliseconds()}
			cancel(replyErr)
		}
	}()
	defer func() { cancel(nil); <-done }()
	if streaming {
		err := streamSpeech(ctx, stream, parts, emit)
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return err
	}
	var ttsCtx context.Context
	var totalSamples int
	var totalMillis int64
	for {
		var part string
		var ok bool
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case part, ok = <-parts:
		}
		if !ok {
			<-done
			if replyErr != nil {
				return replyErr
			}
			if totalSamples == 0 {
				return &pipelineFailure{stage: "synthesizing", err: errors.New("no speakable response")}
			}
			if !emit(ctx, result{phase: "generation_done"}) {
				return context.Canceled
			}
			return nil
		}
		if ttsCtx == nil {
			var stopTTS context.CancelFunc
			ttsCtx, stopTTS = context.WithTimeout(ctx, synthesisBudget)
			defer stopTTS()
		}
		if !emit(ctx, result{phase: "synthesizing"}) {
			return context.Canceled
		}
		start := time.Now()
		pcm, err := speech.Synthesize(ttsCtx, strings.TrimSpace(part), reference)
		totalMillis += max(1, time.Since(start).Milliseconds())
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return &pipelineFailure{stage: "synthesizing", err: err, millis: totalMillis}
		}
		if len(pcm) == 0 || len(pcm) > maxPlaybackSamples-totalSamples {
			return &pipelineFailure{stage: "synthesizing", err: errors.New("empty or oversized segmented playback"), millis: totalMillis}
		}
		totalSamples += len(pcm)
		final := false
		select {
		case <-done:
			if replyErr != nil {
				return replyErr
			}
			final = len(parts) == 0
		default:
		}
		if !emit(ctx, result{phase: "audio_segment", text: part, pcm: pcm, millis: totalMillis, final: final}) {
			return context.Canceled
		}
	}
}
