// Package brain supplies a real Eino ReAct agent and a plain OpenAI-compatible chat mode.
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"aemeath/agent/internal/hamcp"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

type Config struct {
	BaseURL, APIKey, Model, Persona string
	ReasoningEffort                 string
	Tools                           bool
	TavilyAPIKey                    string
	HomeAssistant                   hamcp.Config
	// Memory shares the conversation across Engines; nil keeps it per Engine.
	Memory            *Memory
	ContextWindow     int
	AutoContextWindow bool
	ModelsDev         *ModelCatalog
	ModelsDevProvider string
	ModelsDevModel    string
}
type Host interface {
	Metrics() string
	Quota() string
	SetPage(context.Context, string) error
	SetScreen(context.Context, bool, int) error
	LockComputer(context.Context) error
}

const (
	maxTurns     = 128 // emergency bound; normal retention follows the token budget
	maxTextBytes = 16 << 10
	maxSteps     = 32
)

type Engine struct {
	baseTools          []tool.BaseTool
	haConfig           hamcp.Config
	ha                 homeAssistantClient
	haFactory          homeAssistantFactory
	lifeCtx            context.Context
	lifeCancel         context.CancelFunc
	closed             bool
	model              *openai.ChatModel
	agent              *react.Agent
	persona            string
	timeNow            func() time.Time // local snapshot clock; sampled once per user turn
	gate               chan struct{}
	mu                 sync.Mutex
	generation         uint64
	streamCancel       context.CancelFunc
	history            []*schema.Message // only complete user/final-assistant pairs, no dangling tool calls
	lastActive         time.Time         // last committed turn; persisted as metadata, not an expiry deadline
	memory             *Memory
	summary            string
	stats              ContextStats
	contextConfig      contextConfigKey
	memoryRevision     uint64
	memoryEpoch        uint64
	contextVersion     uint64
	contextPrompt      string
	toolTokens         int
	compDone           chan struct{}
	compCancel         context.CancelFunc
	compWG             sync.WaitGroup
	compressionTimeout time.Duration
}

func New(ctx context.Context, cfg Config, host Host) (*Engine, error) {
	return newWithWebSearch(ctx, cfg, host, newTavilySearch(cfg.TavilyAPIKey))
}

func newWithWebSearch(ctx context.Context, cfg Config, host Host, search webSearchFunc) (*Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	effort, err := NormalizeReasoningEffort(cfg.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	cfg.ReasoningEffort = effort
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" || len(cfg.Model) > 256 {
		return nil, errors.New("brain: configure an OpenAI-compatible model")
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("brain: invalid model base URL")
	}
	// Accept the conventional base URL and an accidentally pasted full completion URL.
	escapedPath := strings.TrimSuffix(u.EscapedPath(), "/chat/completions")
	u.Path, _ = url.PathUnescape(escapedPath)
	u.RawPath = escapedPath
	cfg.BaseURL = u.String()
	persona := strings.TrimSpace(cfg.Persona)
	if persona == "" {
		persona = DefaultPersona
	}
	if len(persona) > maxTextBytes {
		return nil, errors.New("brain: persona exceeds 16384 bytes")
	}
	if _, err := NormalizeContextWindow(cfg.ContextWindow); err != nil {
		return nil, err
	}
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	config := contextConfigFor(cfg)
	e := &Engine{persona: persona, timeNow: time.Now, gate: make(chan struct{}, 1), lifeCtx: lifeCtx, lifeCancel: lifeCancel, haFactory: defaultHomeAssistantFactory, memory: cfg.Memory, stats: contextCapacityStats(config), contextConfig: config, compressionTimeout: 20 * time.Second}
	if e.memory != nil {
		e.memory.mu.Lock()
		e.memory.revision++ // a new connection invalidates older pending memory commits
		for _, t := range e.memory.turns {
			e.history = append(e.history, schema.UserMessage(t.User), schema.AssistantMessage(t.Assistant, nil))
		}
		e.summary, e.lastActive, e.memoryRevision = e.memory.summary, e.memory.updated, e.memory.revision
		e.memoryEpoch = e.memory.epoch
		// Local totals survive restarts; provider totals survive in-process reconnects.
		copyUsage(&e.stats, e.memory.stats)
		e.stats.Compressions, e.stats.DroppedTurns = e.memory.stats.Compressions, e.memory.stats.DroppedTurns
		e.stats.LastCompression = e.memory.stats.LastCompression
		e.memory.mu.Unlock()
	}
	maxTokens := 1024
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{BaseURL: cfg.BaseURL, APIKey: strings.TrimSpace(cfg.APIKey), Model: cfg.Model, ReasoningEffort: openai.ReasoningEffortLevel(cfg.ReasoningEffort), MaxTokens: &maxTokens, HTTPClient: &http.Client{Timeout: 45 * time.Second, Transport: boundedTransport{engine: e}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		lifeCancel()
		return nil, errors.New("brain: cannot initialize OpenAI model")
	}
	e.model = cm
	if cfg.Tools && cfg.HomeAssistant.Enabled {
		e.haConfig = cfg.HomeAssistant
	}
	if cfg.Tools {
		if host == nil {
			lifeCancel()
			return nil, errors.New("brain: device tools require a host")
		}
		e.persona += contextToolInstructions
		e.baseTools = append(deviceTools(host), webSearchTool(search))
		err = e.rebuildAgent(ctx, nil)
		if err != nil {
			lifeCancel()
			return nil, errors.New("brain: cannot initialize Eino ReAct tools")
		}
	}
	e.updateContextLocked()
	if cfg.AutoContextWindow && cfg.ModelsDev != nil {
		cfg.ModelsDev.RefreshAsync(lifeCtx)
	}
	return e, nil
}

// Reply serializes conversation updates, and respects cancellation while queued and in flight.
// Failed/canceled turns never enter history. Clear invalidates in-flight history commits.
func (e *Engine) Reply(ctx context.Context, text string) (string, error) {
	return e.reply(ctx, text, nil, false)
}

// ReplyStream calls onDelta synchronously with final assistant text only. A nil
// callback collects the reply. Callback errors stop generation and are returned
// unchanged. Already delivered deltas cannot be recalled if the stream later fails.
// Clear cancels an active stream; failed/canceled turns never enter history.
func (e *Engine) ReplyStream(ctx context.Context, text string, onDelta func(string) error) (string, error) {
	return e.reply(ctx, text, onDelta, true)
}

func (e *Engine) reply(ctx context.Context, text string, onDelta func(string) error, streaming bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxTextBytes {
		return "", errors.New("brain: input must contain 1 to 16384 bytes")
	}
	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	case <-e.lifeCtx.Done():
		return "", context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, runCancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.lifeCtx, runCancel)
	defer func() { stop(); runCancel() }()
	if e.lifeCtx.Err() != nil {
		return "", context.Canceled
	}
	ctx = context.WithValue(ctx, haAttemptKey{}, &haAttempts{uncertain: make(map[string]bool)})
	if streaming {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		e.mu.Lock()
		e.streamCancel = cancel
		e.mu.Unlock()
		defer func() { e.mu.Lock(); e.streamCancel = nil; e.mu.Unlock() }()
	}
	e.mu.Lock()
	e.syncMemoryLocked()
	generation := e.generation
	ctx = context.WithValue(ctx, requestScopeKey{}, requestScope{generation: generation, epoch: e.memoryEpoch})
	e.mu.Unlock()
	if err := e.resolveContextWindow(ctx, generation); err != nil {
		return "", err
	}
	haPrompt := ""
	if e.haConfig.Enabled {
		haPrompt = e.refreshHomeAssistant(ctx)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// Keep one fresh snapshot for the entire turn, including tool continuations
	// and final speech. It participates in budgeting, but not durable history.
	turnPrompt := haPrompt + timeSnapshotPrompt(e.timeNow())
	persona := e.persona + turnPrompt
	e.mu.Lock()
	e.contextPrompt = turnPrompt
	e.mu.Unlock()
	input, prepErr := e.prepareContext(ctx, persona, text)
	if prepErr != nil {
		return "", prepErr
	}
	var out *schema.Message
	var err error
	if streaming {
		out, err = e.streamReply(ctx, input, onDelta)
	} else if e.agent != nil {
		out, err = e.agent.Generate(ctx, input)
	} else {
		out, err = e.model.Generate(ctx, input)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// SDK errors may contain raw provider bodies or credential-bearing URLs. Never forward them.
	if err != nil {
		var callbackErr *deltaError
		if errors.As(err, &callbackErr) {
			return "", callbackErr.err
		}
		stage := "model"
		if e.agent != nil {
			stage = "agent"
		}
		return "", safeRequestFailure(err, stage)
	}
	if out == nil || len(out.ToolCalls) > 0 || strings.TrimSpace(out.Content) == "" {
		return "", &requestFailure{stage: "final_reply", code: "empty_response"}
	}
	reply := strings.TrimSpace(out.Content)
	if len(reply) > maxTextBytes {
		return "", &requestFailure{stage: "final_reply", code: "response_limit"}
	}
	e.mu.Lock()
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return "", err
	}
	if e.generation == generation {
		e.history = append(e.history, schema.UserMessage(text), schema.AssistantMessage(reply, nil))

		e.lastActive = time.Now()
		e.updateContextLocked()
		e.saveMemoryLocked()
	}
	e.mu.Unlock()
	e.maybeCompress()
	return reply, nil
}

func (e *Engine) rebuildAgent(ctx context.Context, extra []tool.BaseTool) error {
	all := append(append([]tool.BaseTool(nil), e.baseTools...), extra...)
	// Estimate tool schemas before the first request, including newly discovered
	// Home Assistant tools, so history can be compacted before network submission.
	tokens, err := estimateToolSchemas(ctx, all)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.toolTokens = tokens
	e.mu.Unlock()
	for i, candidate := range all {
		if invokable, ok := candidate.(tool.InvokableTool); ok {
			all[i] = diagnosticTool{InvokableTool: invokable}
		}
	}
	agent, err := react.NewAgent(ctx, &react.AgentConfig{ToolCallingModel: &finalStreamModel{base: e.model, decision: e.model}, ToolsConfig: compose.ToolsNodeConfig{Tools: all, ExecuteSequentially: true}, MaxStep: maxSteps})
	if err == nil {
		e.agent = agent
	}
	return err
}

func estimateToolSchemas(ctx context.Context, all []tool.BaseTool) (int, error) {
	wireTools := make([]any, 0, len(all))
	for _, candidate := range all {
		info, err := candidate.Info(ctx)
		if err != nil {
			return 0, err
		}
		params, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return 0, err
		}
		wireTools = append(wireTools, map[string]any{"type": "function", "function": map[string]any{"name": info.Name, "description": info.Desc, "parameters": params}})
	}
	data, err := json.Marshal(wireTools)
	if err != nil {
		return 0, err
	}
	var payload any
	if err = json.Unmarshal(data, &payload); err != nil {
		return 0, err
	}
	return estimateJSON(payload), nil
}

// Close cancels active and queued replies and releases the HA connection.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.lifeCancel()
	client := e.ha
	if e.compCancel != nil {
		e.compCancel()
	}
	e.stats.Compressing = false
	e.publishStatsLocked()
	e.mu.Unlock()
	e.compWG.Wait()
	if client != nil {
		if err := client.Close(); err != nil {
			return errors.New("brain: cannot close Home Assistant connection")
		}
	}
	return nil
}

func (e *Engine) Clear() {
	e.mu.Lock()
	e.history = nil
	e.lastActive = time.Time{}
	e.summary = ""
	e.contextVersion++
	if e.compCancel != nil {
		e.compCancel()
	}
	e.compDone = nil
	e.compCancel = nil
	e.stats = e.emptyContextStatsLocked()
	if e.memory != nil {
		e.memory.Clear()
		e.memory.mu.Lock()
		e.memoryRevision = e.memory.revision
		e.memoryEpoch = e.memory.epoch
		e.memory.mu.Unlock()
	}
	e.generation++
	if e.streamCancel != nil {
		e.streamCancel()
	}
	e.contextPrompt = ""
	e.updateContextLocked()
	e.mu.Unlock()
}

// Cap JSON responses before decoding; cap SSE incrementally without delaying deltas.
// Transport failures are intentionally not retried: a tool-bearing generation may have run.
type boundedTransport struct{ engine *Engine }

func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var scope requestScope
	if t.engine != nil {
		var err error
		req, scope, err = t.engine.budgetRequest(req)
		if err != nil {
			return nil, err
		}
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		failure := safeRequestFailure(err, "")
		var classified *requestFailure
		if errors.As(failure, &classified) && classified.code == "invalid_response" {
			failure = &requestFailure{code: "connection"}
		}
		return nil, failure
	}
	// Classify from the status before the SDK parses an untrusted error body.
	// Do not retain the body, URL, status text or headers in diagnostics.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, httpFailure(res.StatusCode)
	}
	if t.engine != nil {
		t.engine.addLocalUsage(scope.inputTokens, 0, scope)
	}
	const limit = 1 << 20
	if res.StatusCode >= 200 && res.StatusCode < 300 && strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		// Never pre-read SSE: the first sentence must reach TTS before EOF.
		res.Body = &boundedStreamBody{ReadCloser: res.Body, remaining: limit}
		if t.engine != nil {
			res.Body = &usageStreamBody{ReadCloser: res.Body, engine: t.engine, scope: scope}
		}
		return res, nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	res.Body.Close()
	if err != nil {
		return nil, safeRequestFailure(err, "")
	}
	if len(data) > limit {
		return nil, &requestFailure{code: "response_limit"}
	}
	if t.engine != nil {
		t.engine.recordUsage(data, scope)
	}
	// Bound tool fan-out before Eino creates any tool execution tasks.
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		var envelope struct {
			Choices []struct {
				Message struct {
					ToolCalls []json.RawMessage `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			for _, choice := range envelope.Choices {
				if len(choice.Message.ToolCalls) > 8 {
					return nil, &requestFailure{code: "tool_call_limit"}
				}
			}
		}
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	res.ContentLength = int64(len(data))
	return res, nil
}
