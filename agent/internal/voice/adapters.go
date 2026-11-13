package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aemeath/agent/internal/audioinput"
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
	newInput    func(context.Context) (audioinput.Input, error)
	newDetector func(Config) (detector, error)
	newVAD      func(Config) (vad, error)
	newSpeech   func(Config) speechAPI
	newBrain    func(context.Context, Config, Host) (brainAPI, error)
	reference   func(string) ([]int16, error)
	newOutput   func(Config) (consoleOutput, error)
}

func productionDependencies() dependencies {
	return dependencies{
		newInput: audioinput.Open,
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
			var catalog *brain.ModelCatalog
			if c.ModelsDevEnabled && c.ContextWindowMode != "manual" {
				if host, ok := h.(ModelCatalogHost); ok {
					catalog = host.ModelsCatalog()
				} else {
					catalog = brain.DefaultModelCatalog()
				}
			}
			return brain.New(ctx, brain.Config{BaseURL: c.LLMBaseURL, APIKey: c.LLMAPIKey, Model: c.LLMModel, ReasoningEffort: c.ReasoningEffort, ContextWindow: c.ContextWindow, AutoContextWindow: c.ContextWindowMode != "manual", ModelsDev: catalog, ModelsDevProvider: c.ModelsDevProvider, ModelsDevModel: c.ModelsDevModel, Persona: c.Persona, Tools: c.LLMTools, TavilyAPIKey: c.TavilyAPIKey, HomeAssistant: hamcp.Config{Enabled: c.HAMCPEnabled, Endpoint: c.HAMCPURL, Token: c.HAAccessToken}, Memory: memory}, h)
		},
		reference: managedReference,
		newOutput: newConsoleOutput,
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
	context       *brain.ContextStats
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
	workerWithResponses(ctx, cfg, host, d, jobs, out, nil)
}

// responses is a latest-value mailbox for provisional text. Final text stays in
// the ordered result queue so completion/errors can never overtake it.
func workerWithResponses(ctx context.Context, cfg Config, host Host, d dependencies, jobs <-chan work, out chan<- result, responses chan result) {
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
		if source, ok := engine.(interface{ ContextStats() brain.ContextStats }); ok {
			stats := source.ContextStats()
			r.context = &stats
		} else if h, ok := host.(MemoryHost); ok {
			if memory := h.ConversationMemory(); memory != nil {
				stats := memory.ContextStats()
				r.context = &stats
			}
		}
		r.turn = w.turn
		r.generation = w.generation
		if r.phase == "response" && !r.final && responses != nil {
			publishLatestResponse(responses, r)
			return true
		}
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
			publish(w, result{phase: "context"})
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
				text = transcript
				if !publish(w, result{phase: "transcript", text: transcript, millis: time.Since(start).Milliseconds()}) {
					return
				}
			}
			if w.kind == "respeak" {
				if strings.TrimSpace(text) == "" || len(text) > MaxReplayTextBytes {
					fail("synthesizing", errors.New("empty or oversized saved reply"))
					return
				}
				err := segmentedSpeech(w.ctx, savedReplySpeech(text), speech, "", cfg.ReferenceAudio, func(c context.Context, r result) bool {
					cw := w
					cw.ctx = c
					return publish(cw, r)
				})
				if err != nil {
					var failure *pipelineFailure
					if errors.As(err, &failure) {
						publish(w, result{phase: "synthesizing", err: failure.err, millis: failure.millis})
					} else {
						fail("synthesizing", err)
					}
				}
				return
			}
			if w.kind == "utterance" || w.kind == "text" {
				if w.kind == "text" && (text == "" || len(w.text) > 4096) {
					fail("thinking", errors.New("empty or oversized text input"))
					return
				}
				if cfg.LLMModel == "" {
					fail("configuration", errors.New("LLM model is missing"))
					return
				}
				if !publish(w, result{phase: "thinking"}) {
					return
				}
				stageStarted = time.Now()
				agentCtx, stopAgent := context.WithTimeout(w.ctx, agentBudget)
				defer stopAgent()
				var err error
				if engine == nil {
					engine, err = d.newBrain(agentCtx, cfg, host)
					if err != nil {
						fail("thinking", err)
						return
					}
				}
				stopAgent()
				err = segmentedSpeech(w.ctx, engine, speech, text, cfg.ReferenceAudio, func(c context.Context, r result) bool {
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
