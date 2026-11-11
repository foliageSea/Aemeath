package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

type requestScopeKey struct{}
type requestScope struct {
	generation uint64
	epoch      uint64
	summary    bool
}

// Count the actual serialized request payload, including tool definitions,
// arguments, reasoning metadata and tool results, without storing any payload.
func estimateJSON(v any) int {
	switch x := v.(type) {
	case string:
		return estimateTokens(x)
	case []any:
		n := 0
		for _, item := range x {
			n += estimateJSON(item)
		}
		return n
	case map[string]any:
		n := 6
		for k, item := range x {
			n += estimateTokens(k) + estimateJSON(item)
		}
		return n
	case nil:
		return 0
	default:
		return 1
	}
}

func (e *Engine) budgetRequest(req *http.Request) (*http.Request, requestScope, error) {
	scope, _ := req.Context().Value(requestScopeKey{}).(requestScope)
	var body io.ReadCloser
	var err error
	if req.GetBody != nil {
		body, err = req.GetBody()
	} else {
		body = req.Body
	}
	if err != nil || body == nil {
		return nil, scope, &requestFailure{code: "invalid_request"}
	}
	data, err := io.ReadAll(io.LimitReader(body, 8<<20))
	body.Close()
	if req.GetBody == nil {
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(data))
	}
	if err != nil || len(data) >= 8<<20 {
		return nil, scope, &requestFailure{code: "context_limit"}
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return nil, scope, &requestFailure{code: "invalid_request"}
	}
	tokens := 3 + estimateJSON(payload["messages"]) + estimateJSON(payload["tools"])
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.requestCurrentLocked(scope) {
		return nil, scope, context.Canceled
	}
	if scope.generation == e.generation && !scope.summary {
		if tools, ok := payload["tools"]; ok {
			e.toolTokens = estimateJSON(tools)
		}
		e.stats.EstimatedTokens = tokens
		e.stats.UsagePercent = 100 * float64(tokens) / float64(e.stats.InputBudget)
		e.publishStatsLocked()
	}
	// Allow tool-loop growth into its reserved 10%, but always retain output and
	// safety space. Never replay a tool-bearing request or remove live tool pairs.
	hardLimit := e.stats.InputBudget + e.stats.ContextWindow/10
	if e.stats.ContextInputLimit > 0 {
		hardLimit = min(hardLimit, e.stats.ContextInputLimit)
	}
	if tokens > hardLimit {
		return nil, scope, &requestFailure{code: "context_limit"}
	}
	return req, scope, nil
}

type providerUsage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Total      int `json:"total_tokens"`
}

func parseUsage(data []byte) *providerUsage {
	var response struct {
		Usage *providerUsage `json:"usage"`
	}
	if json.Unmarshal(data, &response) != nil || response.Usage == nil {
		return nil
	}
	u := response.Usage
	const limit = 1 << 30
	if u.Prompt < 0 || u.Completion < 0 || u.Total < 0 || u.Prompt > limit || u.Completion > limit || u.Total > limit {
		return nil
	}
	if u.Total == 0 {
		u.Total = u.Prompt + u.Completion
	}
	return u
}
func (e *Engine) recordUsage(data []byte, scope requestScope) {
	if u := parseUsage(data); u != nil {
		e.addUsage(*u, scope)
	}
}
func (e *Engine) addUsage(u providerUsage, scope requestScope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Billing belongs to the conversation epoch, independent of which connection
	// owns the latest transcript. Closing a connection does not erase spent tokens.
	if e.memory != nil {
		m := e.memory
		m.mu.Lock()
		defer m.mu.Unlock()
		if scope.epoch != m.epoch {
			return
		}
		m.stats.UsageAvailable = true
		m.stats.PromptTokens += u.Prompt
		m.stats.CompletionTokens += u.Completion
		m.stats.TotalTokens += u.Total
		if e.memoryEpoch == m.epoch {
			copyUsage(&e.stats, m.stats)
		}
		return
	}
	if scope.generation != e.generation {
		return
	}
	e.stats.UsageAvailable = true
	e.stats.PromptTokens += u.Prompt
	e.stats.CompletionTokens += u.Completion
	e.stats.TotalTokens += u.Total
}

// Observe SSE incrementally without delaying or altering the bytes returned to
// the SDK. Keep only a bounded current line and the last usage snapshot: some
// compatible providers repeat cumulative usage in every chunk.
type usageStreamBody struct {
	io.ReadCloser
	engine   *Engine
	scope    requestScope
	mu       sync.Mutex
	line     []byte
	usage    *providerUsage
	finished bool
}

func (b *usageStreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if !b.finished {
		for _, c := range p[:n] {
			if c == '\n' {
				b.parseLine()
				b.line = b.line[:0]
			} else if len(b.line) < 1<<20 {
				b.line = append(b.line, c)
			}
		}
	}
	b.mu.Unlock()
	if err != nil {
		b.finish()
	}
	return n, err
}
func (b *usageStreamBody) parseLine() {
	line := bytes.TrimSpace(b.line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	if u := parseUsage(bytes.TrimSpace(line[5:])); u != nil {
		b.usage = u
	}
}
func (b *usageStreamBody) finish() {
	b.mu.Lock()
	if b.finished {
		b.mu.Unlock()
		return
	}
	b.parseLine()
	b.finished = true
	u := b.usage
	b.line = nil
	b.mu.Unlock()
	if u != nil {
		b.engine.addUsage(*u, b.scope)
	}
}
func (b *usageStreamBody) Close() error { err := b.ReadCloser.Close(); b.finish(); return err }
