package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/hamcp"
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
			return wake.New(wake.Config{ModelDir: c.WakeModelDir, Threshold: c.WakeThreshold, Phrase: c.WakePhrase})
		},
		newVAD:    func(c Config) (vad, error) { return wake.NewVAD(c.WakeModelDir) },
		newSpeech: configuredSpeech,
		newBrain: func(ctx context.Context, c Config, h Host) (brainAPI, error) {
			var memory *brain.Memory
			if m, ok := h.(MemoryHost); ok {
				memory = m.ConversationMemory()
			}
			return brain.New(ctx, brain.Config{BaseURL: c.LLMBaseURL, APIKey: c.LLMAPIKey, Model: c.LLMModel, Persona: c.Persona, Tools: c.LLMTools, TavilyAPIKey: c.TavilyAPIKey, HomeAssistant: hamcp.Config{Enabled: c.HAMCPEnabled, Endpoint: c.HAMCPURL, Token: c.HAAccessToken}, Memory: memory}, h)
		},
		reference: managedReference,
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
	generation    uint64
	toolCall      *brain.ToolCall
	homeAssistant *hamcp.Status
	turn          uint32
	phase, text   string
	pcm           []int16
	millis        int64
	err           error
	final         bool
	samples       int
}

// One worker serializes all history access, including canceled Reply and Clear.
// Its pending queue has one slot; the event loop replaces superseded work.
func worker(ctx context.Context, cfg Config, host Host, d dependencies, jobs <-chan work, out chan<- result) {
	var engine brainAPI
	defer func() {
		if closer, ok := engine.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()
	var speech speechAPI
	var generation uint64
	publish := func(w work, r result) bool {
		if w.ctx.Err() != nil || ctx.Err() != nil {
			return false
		}
		r.turn = w.turn
		r.generation = w.generation
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
			} else if h, ok := host.(MemoryHost); ok {
				// After a restart the model is lazy-created. Clear must still
				// delete restored memory before the first model request.
				if memory := h.ConversationMemory(); memory != nil {
					memory.Clear()
				}
			}
			generation = w.generation
		}
		if w.kind == "clear" {
			continue
		}
		func() {
			stageStarted := time.Now()
			fail := func(stage string, err error) {
				publish(w, result{phase: stage, err: err, millis: max(1, time.Since(stageStarted).Milliseconds())})
			}
			if w.kind == "reference" {
				pcm, err := d.reference(cfg.ReferenceAudio)
				if err != nil {
					fail("reference", err)
					return
				}
				publish(w, result{phase: "audio", pcm: pcm})
				return
			}
			if cfg.TTSProvider == "fish" && (cfg.FishAPIKey == "" || (cfg.FishReferenceID == "" && cfg.FishReferenceText == "")) {
				fail("configuration", errors.New("请填写 Fish API Key 和 reference_id，或提供参考音频对应文字"))
				return
			}
			if cfg.MiMoAPIKey == "" && (w.kind == "utterance" || cfg.TTSProvider != "fish") {
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
				stageStarted = start
				recognitionCtx, stopRecognition := context.WithTimeout(w.ctx, recognitionBudget)
				transcript, err := speech.Recognize(recognitionCtx, w.pcm)
				stopRecognition()
				if err != nil {
					fail("recognizing", err)
					return
				}
				rawTranscript := strings.TrimSpace(transcript)
				transcript = stripWakePhrasePrefix(rawTranscript, cfg.WakePhrase)
				if transcript == "" {
					if rawTranscript != "" {
						// Hearing only the wake invocation is a normal invitation to listen,
						// not a question for the Agent and not a provider transport failure.
						publish(w, result{phase: "wake_only", millis: time.Since(start).Milliseconds()})
					} else {
						fail("recognizing", errors.New("未识别到有效语音，请重新唤醒后说出问题"))
					}
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
				stageStarted = start
				agentCtx, stopAgent := context.WithTimeout(w.ctx, agentBudget)
				defer stopAgent()
				if engine == nil {
					engine, err = d.newBrain(agentCtx, cfg, host)
					if err != nil {
						fail("thinking", err)
						return
					}
				}
				stopAgent()
				err = segmentedSpeech(w.ctx, engine, speech, transcript, cfg.ReferenceAudio, func(c context.Context, r result) bool {
					chunkWork := w
					chunkWork.ctx = c
					return publish(chunkWork, r)
				})
				if err != nil {
					var failure *pipelineFailure
					if errors.As(err, &failure) {
						publish(w, result{phase: failure.stage, err: failure.err, millis: failure.millis})
					} else {
						fail("thinking", err)
					}
				}
				return
			}
			if text == "" {
				text = "你好，我是爱弥斯，很高兴见到你。"
			}
			if len(text) > maxTextBytes/2 {
				fail("synthesizing", fmt.Errorf("preview text too long"))
				return
			}
			if stream, ok := speech.(streamingSpeech); ok {
				parts := make(chan string, 1)
				parts <- text
				close(parts)
				err := streamSpeech(w.ctx, stream, parts, func(c context.Context, r result) bool { cw := w; cw.ctx = c; return publish(cw, r) })
				if err != nil {
					var failure *pipelineFailure
					if errors.As(err, &failure) {
						publish(w, result{phase: failure.stage, err: failure.err, millis: failure.millis})
					} else {
						fail("synthesizing", err)
					}
				}
				return
			}
			if !publish(w, result{phase: "synthesizing"}) {
				return
			}
			start := time.Now()
			stageStarted = start
			synthesisCtx, stopSynthesis := context.WithTimeout(w.ctx, synthesisBudget)
			pcm, err := speech.Synthesize(synthesisCtx, text, cfg.ReferenceAudio)
			stopSynthesis()
			if err != nil {
				fail("synthesizing", err)
				return
			}
			publish(w, result{phase: "audio", pcm: pcm, millis: time.Since(start).Milliseconds()})
		}()
	}
}
