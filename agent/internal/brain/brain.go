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

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

type Config struct {
	BaseURL, APIKey, Model, Persona string
	Tools                           bool
}
type Host interface {
	Metrics() string
	Quota() string
	SetPage(context.Context, string) error
	SetScreen(context.Context, bool, int) error
	LockComputer(context.Context) error
}

const (
	maxTurns       = 10
	maxTextBytes   = 16 << 10
	maxSteps       = 8
	defaultPersona = "你是爱弥斯，一位温暖、机敏的桌面语音助手。用简洁自然的中文回答，适合直接朗读。只有在用户请求或回答确实需要时才调用工具；不要声称执行了没有成功执行的操作。"
)

type Engine struct {
	model        *openai.ChatModel
	agent        *react.Agent
	persona      string
	gate         chan struct{}
	mu           sync.Mutex
	generation   uint64
	streamCancel context.CancelFunc
	history      []*schema.Message // only complete user/final-assistant pairs, no dangling tool calls
}

func New(ctx context.Context, cfg Config, host Host) (*Engine, error) {
	return newWithTime(ctx, cfg, host, defaultCurrentTime)
}

func newWithTime(ctx context.Context, cfg Config, host Host, currentTime currentTimeFunc) (*Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	cfg.BaseURL = u.String()
	persona := strings.TrimSpace(cfg.Persona)
	if persona == "" {
		persona = defaultPersona
	}
	if len(persona) > maxTextBytes {
		return nil, errors.New("brain: persona exceeds 16384 bytes")
	}
	maxTokens := 1024
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{BaseURL: cfg.BaseURL, APIKey: strings.TrimSpace(cfg.APIKey), Model: cfg.Model, MaxTokens: &maxTokens, HTTPClient: &http.Client{Timeout: 45 * time.Second, Transport: boundedTransport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		return nil, errors.New("brain: cannot initialize OpenAI model")
	}
	e := &Engine{model: cm, persona: persona, gate: make(chan struct{}, 1)}
	if cfg.Tools {
		if host == nil {
			return nil, errors.New("brain: device tools require a host")
		}
		e.persona += "\n" + currentTimeInstructions
		e.agent, err = react.NewAgent(ctx, &react.AgentConfig{ToolCallingModel: &finalStreamModel{base: cm, decision: cm}, ToolsConfig: compose.ToolsNodeConfig{Tools: append(deviceTools(host), currentTimeTool(currentTime)), ExecuteSequentially: true}, MaxStep: maxSteps})
		if err != nil {
			return nil, errors.New("brain: cannot initialize Eino ReAct tools")
		}
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
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
	generation := e.generation
	hist := append([]*schema.Message(nil), e.history...)
	e.mu.Unlock()
	// At most ten turns are sent, including the current request.
	if len(hist) > 2*(maxTurns-1) {
		hist = hist[len(hist)-2*(maxTurns-1):]
	}
	input := make([]*schema.Message, 0, len(hist)+2)
	persona := e.persona
	if e.agent != nil {
		persona += "\n本机日期提示（未进行外部校时）：" + time.Now().Format("2006-01-02")
	}
	input = append(input, schema.SystemMessage(persona))
	input = append(input, hist...)
	input = append(input, schema.UserMessage(text))
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
		return "", errors.New("brain: model or tool request failed (check connection, model and tool step limit)")
	}
	if out == nil || len(out.ToolCalls) > 0 || strings.TrimSpace(out.Content) == "" {
		return "", errors.New("brain: model did not return a final text reply")
	}
	reply := strings.TrimSpace(out.Content)
	if len(reply) > maxTextBytes {
		return "", errors.New("brain: model reply exceeds 16384 bytes")
	}
	e.mu.Lock()
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return "", err
	}
	if e.generation == generation {
		e.history = append(e.history, schema.UserMessage(text), schema.AssistantMessage(reply, nil))
		if len(e.history) > 2*maxTurns {
			e.history = append([]*schema.Message(nil), e.history[len(e.history)-2*maxTurns:]...)
		}
	}
	e.mu.Unlock()
	return reply, nil
}

func (e *Engine) Clear() {
	e.mu.Lock()
	e.history = nil
	e.generation++
	if e.streamCancel != nil {
		e.streamCancel()
	}
	e.mu.Unlock()
}

// Cap JSON responses before decoding; cap SSE incrementally without delaying deltas.
// Transport failures are intentionally not retried: a tool-bearing generation may have run.
type boundedTransport struct{}

func (boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, errors.New("model transport failed")
	}
	const limit = 1 << 20
	if res.StatusCode >= 200 && res.StatusCode < 300 && strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		// Never pre-read SSE: the first sentence must reach TTS before EOF.
		res.Body = &boundedStreamBody{ReadCloser: res.Body, remaining: limit}
		return res, nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	res.Body.Close()
	if err != nil || len(data) > limit {
		return nil, errors.New("model response exceeds limit or cannot be read")
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
					return nil, errors.New("model returned too many tool calls")
				}
			}
		}
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	res.ContentLength = int64(len(data))
	return res, nil
}
