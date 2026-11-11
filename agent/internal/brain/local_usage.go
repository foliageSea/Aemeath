package brain

import (
	"encoding/json"
	"fmt"
)

// Keep an unfinished ASCII run across deltas so splitting a word into many
// chunks does not inflate its estimate. No generated text is retained.
type localTokenCounter struct {
	complete, asciiRun int
}

func (c *localTokenCounter) add(text string) {
	for _, r := range text {
		if r < 128 && ((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ') {
			c.asciiRun++
			continue
		}
		c.complete += (c.asciiRun + 2) / 3
		c.asciiRun = 0
		if r < 128 {
			c.complete++
		} else {
			c.complete += 2
		}
	}
}

func (c *localTokenCounter) tokens() int { return c.complete + (c.asciiRun+2)/3 }

// Observe visible completion fields, not SSE envelopes or repeated provider
// usage snapshots. Each content/tool field has its own incremental counter.
type localOutputCounter struct{ fields map[string]*localTokenCounter }

func (c *localOutputCounter) value(key string, value any) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return
		}
		if c.fields == nil {
			c.fields = make(map[string]*localTokenCounter)
		}
		if c.fields[key] == nil {
			c.fields[key] = &localTokenCounter{}
		}
		c.fields[key].add(v)
	case []any:
		for i, item := range v {
			c.value(fmt.Sprintf("%s/%d", key, i), item)
		}
	case map[string]any:
		for name, item := range v {
			c.value(key+"/"+name, item)
		}
	}
}

func (c *localOutputCounter) observe(data []byte) {
	var response struct {
		Choices []struct {
			Index   int            `json:"index"`
			Message map[string]any `json:"message"`
			Delta   map[string]any `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &response) != nil {
		return
	}
	for _, choice := range response.Choices {
		message := choice.Message
		if message == nil {
			message = choice.Delta
		}
		prefix := fmt.Sprint(choice.Index)
		for _, field := range []string{"content", "reasoning_content", "reasoning", "function_call"} {
			c.value(prefix+"/"+field, message[field])
		}
		calls, _ := message["tool_calls"].([]any)
		for i, item := range calls {
			call, ok := item.(map[string]any)
			if !ok {
				continue
			}
			index := i
			if n, ok := call["index"].(float64); ok {
				index = int(n)
			}
			c.value(fmt.Sprintf("%s/tool/%d", prefix, index), call["function"])
		}
	}
}

func (c *localOutputCounter) tokens() int {
	total := 0
	for _, field := range c.fields {
		total += field.tokens()
	}
	return total
}

func (e *Engine) addLocalUsage(input, output int, scope requestScope) {
	if input == 0 && output == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	add := func(stats *ContextStats) {
		stats.LocalPromptTokens += input
		stats.LocalCompletionTokens += output
		stats.LocalTotalTokens = stats.LocalPromptTokens + stats.LocalCompletionTokens
	}
	if e.memory != nil {
		m := e.memory
		m.mu.Lock()
		defer m.mu.Unlock()
		if scope.epoch != m.epoch {
			return
		}
		add(&m.stats)
		if e.memoryEpoch == m.epoch {
			copyUsage(&e.stats, m.stats)
		}
		// Commit input on acceptance and output at response completion, even if
		// the turn later fails or is canceled before entering transcript history.
		m.persist()
		return
	}
	if scope.generation != e.generation {
		return
	}
	add(&e.stats)
}
