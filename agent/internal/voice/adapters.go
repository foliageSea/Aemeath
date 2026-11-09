package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voiceapi"
	"aemeath/agent/internal/wake"
)

type detector interface {
	Feed([]int16) (bool, error)
	Reset()
	Close()
	KeywordEndSamples() int64
}
type vad interface {
	Speech([]int16) (bool, error)
	Reset()
	Close()
}
type speechAPI interface {
	Recognize(context.Context, []int16) (string, error)
	Synthesize(context.Context, string, string) ([]int16, error)
}
type brainAPI interface {
	Reply(context.Context, string) (string, error)
	Clear()
}

// Factories are injected per session in tests, never mutated package globals.
type dependencies struct {
	newDetector func(Config) (detector, error)
	newVAD      func(Config) (vad, error)
	newSpeech   func(Config) speechAPI
	newBrain    func(context.Context, Config, Host) (brainAPI, error)
	reference   func(string) ([]int16, error)
}

func productionDependencies() dependencies {
	return dependencies{
		newDetector: func(c Config) (detector, error) {
			return wake.New(wake.Config{ModelDir: c.WakeModelDir, Threshold: c.WakeThreshold})
		},
		newVAD:    func(c Config) (vad, error) { return wake.NewVAD(c.WakeModelDir) },
		newSpeech: func(c Config) speechAPI { return voiceapi.New(c.MiMoBaseURL, c.MiMoAPIKey) },
		newBrain: func(ctx context.Context, c Config, h Host) (brainAPI, error) {
			return brain.New(ctx, brain.Config{BaseURL: c.LLMBaseURL, APIKey: c.LLMAPIKey, Model: c.LLMModel, Persona: c.Persona, Tools: c.LLMTools}, h)
		},
		reference: voiceapi.ReferencePCM,
	}
}

type work struct {
	ctx        context.Context
	turn       uint32
	generation uint64
	kind, text string
	pcm        []int16
}
type result struct {
	turn        uint32
	phase, text string
	pcm         []int16
	millis      int64
	err         error
}

// One worker serializes all history access, including canceled Reply and Clear.
// Its pending queue has one slot; the event loop replaces superseded work.
func worker(ctx context.Context, cfg Config, host Host, d dependencies, jobs <-chan work, out chan<- result) {
	var engine brainAPI
	var speech speechAPI
	var generation uint64
	publish := func(w work, r result) bool {
		if w.ctx.Err() != nil || ctx.Err() != nil {
			return false
		}
		r.turn = w.turn
		select {
		case out <- r:
			return true
		case <-w.ctx.Done():
			return false
		case <-ctx.Done():
			return false
		}
	}
	for {
		var w work
		select {
		case <-ctx.Done():
			return
		case w = <-jobs:
		}
		if w.ctx.Err() != nil {
			continue
		}
		if generation != w.generation {
			if engine != nil {
				engine.Clear()
			}
			generation = w.generation
		}
		if w.kind == "clear" {
			continue
		}
		func() {
			fail := func(stage string, err error) { publish(w, result{phase: stage, err: err}) }
			if w.kind == "reference" {
				pcm, err := d.reference(cfg.ReferenceAudio)
				if err != nil {
					fail("reference", err)
					return
				}
				publish(w, result{phase: "audio", pcm: pcm})
				return
			}
			if cfg.MiMoAPIKey == "" {
				fail("configuration", errors.New("MiMo API key is missing"))
				return
			}
			if speech == nil {
				speech = d.newSpeech(cfg)
			}
			text := strings.TrimSpace(w.text)
			if w.kind == "utterance" {
				if !publish(w, result{phase: "recognizing"}) {
					return
				}
				start := time.Now()
				transcript, err := speech.Recognize(w.ctx, w.pcm)
				if err != nil {
					fail("recognizing", err)
					return
				}
				transcript = stripWakePrefix(transcript)
				if transcript == "" {
					fail("recognizing", errors.New("no speech recognized"))
					return
				}
				if len(transcript) > maxTextBytes/2 {
					fail("recognizing", errors.New("transcript too long"))
					return
				}
				if !publish(w, result{phase: "transcript", text: transcript, millis: time.Since(start).Milliseconds()}) {
					return
				}
				if cfg.LLMModel == "" {
					fail("configuration", errors.New("LLM model is missing"))
					return
				}
				if !publish(w, result{phase: "thinking"}) {
					return
				}
				start = time.Now()
				if engine == nil {
					engine, err = d.newBrain(w.ctx, cfg, host)
					if err != nil {
						fail("thinking", err)
						return
					}
				}
				text, err = engine.Reply(w.ctx, transcript)
				if err != nil {
					fail("thinking", err)
					return
				}
				text = strings.TrimSpace(text)
				if text == "" || len(text) > maxTextBytes/2 {
					fail("thinking", errors.New("empty or oversized assistant response"))
					return
				}
				if !publish(w, result{phase: "response", text: text, millis: time.Since(start).Milliseconds()}) {
					return
				}
			}
			if text == "" {
				text = "你好，我是爱弥斯，很高兴见到你。"
			}
			if len(text) > maxTextBytes/2 {
				fail("synthesizing", fmt.Errorf("preview text too long"))
				return
			}
			if !publish(w, result{phase: "synthesizing"}) {
				return
			}
			start := time.Now()
			pcm, err := speech.Synthesize(w.ctx, text, cfg.ReferenceAudio)
			if err != nil {
				fail("synthesizing", err)
				return
			}
			publish(w, result{phase: "audio", pcm: pcm, millis: time.Since(start).Milliseconds()})
		}()
	}
}
