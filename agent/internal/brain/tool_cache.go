package brain

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type toolResultCacheContextKey struct{}

type toolResultCache struct {
	mu      sync.Mutex
	entries map[string]*toolResultCacheEntry
}

type toolResultCacheEntry struct {
	done   chan struct{}
	result string
	err    error
}

// withToolResultCache starts a fresh cache for one user turn, even when ctx
// already carries a cache. Descendant contexts share only this turn's entries.
func withToolResultCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolResultCacheContextKey{}, &toolResultCache{
		entries: make(map[string]*toolResultCacheEntry),
	})
}

// cachedToolResult reuses only explicitly allowlisted read-only tool results.
// The first caller runs synchronously without holding the cache mutex; callers
// joining the same in-flight key may stop waiting when their own context ends.
func cachedToolResult(ctx context.Context, name, args string, run func() (string, error)) (string, error) {
	cache, _ := ctx.Value(toolResultCacheContextKey{}).(*toolResultCache)
	if cache == nil {
		return run()
	}
	key, ok := toolResultCacheKey(name, args)
	if !ok {
		return run()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	cache.mu.Lock()
	if entry, exists := cache.entries[key]; exists {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-entry.done:
			// Cancellation wins even when the cached result is already ready.
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return entry.result, entry.err
		}
	}
	entry := &toolResultCacheEntry{done: make(chan struct{})}
	cache.entries[key] = entry
	cache.mu.Unlock()

	result, err := run()
	if ctx.Err() != nil {
		result, err = "", ctx.Err()
	}
	cacheable := err == nil && toolResultCacheSuccessful(result)

	cache.mu.Lock()
	entry.result, entry.err = result, err
	if !cacheable {
		delete(cache.entries, key)
	}
	close(entry.done)
	cache.mu.Unlock()
	return result, err
}

func toolResultCacheKey(name, args string) (string, bool) {
	// Do not let canonicalization hide the invocation argument-size guard.
	// Even whitespace contributes to the original tool argument byte limit.
	if len(args) > 4096 {
		return "", false
	}
	switch name {
	case "get_metrics", "get_quota", "web_search":
	default:
		return "", false
	}

	// Decode into an object, not interface{}: null, arrays and primitives must
	// never alias {}. UseNumber avoids merging distinct large integers/decimals.
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.UseNumber()
	var params map[string]any
	if decoder.Decode(&params) != nil || params == nil {
		return "", false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return "", false
	}

	if name == "web_search" {
		// Validate the original input before normalization. In particular, a
		// string/decimal limit or a duplicate field with an invalid earlier
		// value must not hit a successful, correctly typed invocation's key.
		var input struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		decoder = json.NewDecoder(strings.NewReader(args))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			return "", false
		}
		for _, r := range input.Query {
			if unicode.IsControl(r) && !unicode.IsSpace(r) {
				return "", false
			}
		}
		input.Query = strings.Join(strings.Fields(input.Query), " ")
		if input.Limit == 0 {
			input.Limit = 3
		}
		if input.Query == "" || !utf8.ValidString(input.Query) || utf8.RuneCountInString(input.Query) > 256 || len(input.Query) > 1024 || input.Limit < 1 || input.Limit > 5 {
			return "", false
		}
		params = map[string]any{"query": input.Query, "limit": input.Limit}
	}

	// encoding/json sorts object keys, including nested objects. The original
	// argument string is still passed to run by its caller, never rewritten.
	canonical, err := json.Marshal(params)
	if err != nil {
		return "", false
	}
	return name + "\x00" + string(canonical), true
}

func toolResultCacheSuccessful(result string) bool {
	// Metrics/quota may be plain text; only an explicit JSON failure marker
	// turns a nil Go error into a non-cacheable result, matching tool observers.
	var outcome struct {
		OK *bool `json:"ok"`
	}
	return json.Unmarshal([]byte(result), &outcome) != nil || outcome.OK == nil || *outcome.OK
}
