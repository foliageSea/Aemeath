package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// These are metadata lookups on the configured provider only. Neither model
// name guesses nor token usage can determine a server's actual context limit.
type modelMetadataConfig struct{ baseURL, apiKey, model string }

const modelMetadataLimit = 8 << 20
const modelMetadataTimeout = 2 * time.Second

func (e *Engine) resolveContextWindow(ctx context.Context, generation uint64) error {
	e.mu.Lock()
	if e.contextMetadata == nil || e.contextDiscoveryDone {
		e.mu.Unlock()
		return nil
	}
	config := *e.contextMetadata
	epoch := e.memoryEpoch
	e.stats.ContextWindowSource = "detecting"
	e.publishStatsLocked()
	e.mu.Unlock()
	window := discoverModelContext(ctx, config)
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.requestCurrentLocked(requestScope{generation: generation, epoch: epoch}) {
		return context.Canceled
	}
	e.contextDiscoveryDone = true
	e.stats.ContextWindowSource = "fallback"
	if window > 0 {
		budget := newContextStats(window)
		e.stats.ContextWindow, e.stats.InputBudget, e.stats.ReservedTokens = budget.ContextWindow, budget.InputBudget, budget.ReservedTokens
		e.stats.ContextWindowSource = "provider"
	}
	e.stats.ContextWindowModel = config.model
	e.updateContextLocked()
	return nil
}

// Try the exact detail endpoint, then the standard list endpoint. One bounded
// lookup per Engine keeps normal turns fast and reconnects refresh metadata.
func discoverModelContext(parent context.Context, config modelMetadataConfig) int {
	ctx, cancel := context.WithTimeout(parent, modelMetadataTimeout)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base, err := url.Parse(config.baseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "http" && base.Scheme != "https") {
		return 0
	}
	list := *base
	// Trim literal URL separators only; an encoded %2F can belong to a tenant
	// prefix. Derive decoded Path from RawPath so String never drops escaping.
	list.RawPath = strings.TrimRight(base.EscapedPath(), "/") + "/models"
	list.Path, _ = url.PathUnescape(list.RawPath)
	detail := list
	detail.Path += "/" + config.model
	detail.RawPath += "/" + url.PathEscape(config.model)
	for _, endpoint := range []url.URL{detail, list} {
		if ctx.Err() != nil {
			return 0
		}
		attempt, stop := context.WithTimeout(ctx, 900*time.Millisecond)
		window := fetchModelContext(attempt, client, endpoint.String(), config)
		stop()
		if window > 0 {
			return window
		}
	}
	return 0
}
func fetchModelContext(ctx context.Context, client *http.Client, endpoint string, config modelMetadataConfig) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Accept", "application/json")
	if config.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+config.apiKey)
	}
	response, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, modelMetadataLimit+1))
	if err != nil || len(data) > modelMetadataLimit {
		return 0
	}
	return modelContextFromJSON(data, config.model)
}

func modelContextFromJSON(data []byte, model string) int {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root any
	if decoder.Decode(&root) != nil {
		return 0
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return 0
	}
	best := 0
	consider := func(value any) {
		object, ok := value.(map[string]any)
		if !ok || object["id"] != model {
			return
		}
		n := contextCapacity(object)
		// Conflicting exact entries must never enlarge the smallest stated limit.
		if n > 0 && (best == 0 || n < best) {
			best = n
		}
	}
	switch object := root.(type) {
	case map[string]any:
		consider(object)
		switch data := object["data"].(type) {
		case map[string]any:
			consider(data)
		case []any:
			for _, entry := range data {
				consider(entry)
			}
		}
	case []any:
		for _, entry := range object {
			consider(entry)
		}
	}
	return best
}

// Only explicitly named context-limit fields qualify. max_tokens and
// max_completion_tokens describe output, and usage is consumption, not capacity.
func contextCapacity(object map[string]any) int {
	best := 0
	fields := []string{"context_length", "context_window", "max_context_length", "max_model_len", "max_position_embeddings"}
	read := func(values map[string]any) {
		for _, name := range fields {
			n := contextCapacityNumber(values[name])
			if n > 0 && (best == 0 || n < best) {
				best = n
			}
		}
	}
	read(object)
	for _, name := range []string{"top_provider", "limits", "metadata"} {
		if child, ok := object[name].(map[string]any); ok {
			read(child)
		}
	}
	return best
}
func contextCapacityNumber(value any) int {
	var n float64
	switch v := value.(type) {
	case json.Number:
		var err error
		n, err = strconv.ParseFloat(string(v), 64)
		if err != nil {
			return 0
		}
	case string:
		// Accept numeric strings, never interpret ambiguous labels such as "128K".
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0
		}
		n = float64(i)
	default:
		return 0
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 4096 || n > 2097152 {
		return 0
	}
	return int(n)
}
