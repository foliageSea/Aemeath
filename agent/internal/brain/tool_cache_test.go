package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolResultCacheIsScopedToEachTurn(t *testing.T) {
	parent := context.Background()
	first := withToolResultCache(parent)
	second := withToolResultCache(first)
	var calls int
	run := func() (string, error) {
		calls++
		return fmt.Sprintf("result-%d", calls), nil
	}
	for _, test := range []struct {
		ctx  context.Context
		want string
	}{
		{first, "result-1"},
		{first, "result-1"},
		{second, "result-2"},
		{second, "result-2"},
		{first, "result-1"},
		{withToolResultCache(parent), "result-3"},
	} {
		got, err := cachedToolResult(test.ctx, "get_metrics", `{}`, run)
		if err != nil || got != test.want {
			t.Fatalf("result=%q err=%v, want %q", got, err, test.want)
		}
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want one per turn", calls)
	}
	child, cancel := context.WithCancel(first)
	defer cancel()
	got, err := cachedToolResult(child, "get_metrics", `{}`, run)
	if err != nil || got != "result-1" || calls != 3 {
		t.Fatalf("descendant failed to reuse its turn: %q %v calls=%d", got, err, calls)
	}
}

func TestToolResultCacheWithoutContextPassesThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wantErr := errors.New("original error")
	var calls int
	for range 2 {
		got, err := cachedToolResult(ctx, "get_metrics", `{}`, func() (string, error) {
			calls++
			return "original result", wantErr
		})
		if got != "original result" || err != wantErr {
			t.Fatalf("non-cache context altered the invocation: %q %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
}

func TestToolResultCacheCanonicalObjectAndNumberPrecision(t *testing.T) {
	ctx := withToolResultCache(context.Background())
	var calls int
	run := func() (string, error) {
		calls++
		return fmt.Sprint(calls), nil
	}
	for _, args := range []string{
		`{"z":[{"b":2,"a":1}],"n":9007199254740993,"decimal":0.123456789012345678901}`,
		" \n { \"decimal\": 0.123456789012345678901, \"n\":9007199254740993, \"z\":[{\"a\":1,\"b\":2}] } \t",
	} {
		got, err := cachedToolResult(ctx, "get_metrics", args, run)
		if err != nil || got != "1" {
			t.Fatalf("canonical object did not reuse: %q %v", got, err)
		}
	}
	for _, args := range []string{
		`{"z":[{"b":2,"a":1}],"n":9007199254740992,"decimal":0.123456789012345678901}`,
		`{"z":[{"b":2,"a":1}],"n":9007199254740993,"decimal":0.123456789012345678902}`,
	} {
		if _, err := cachedToolResult(ctx, "get_metrics", args, run); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("distinct precise numbers aliased: calls=%d, want 3", calls)
	}
	if _, err := cachedToolResult(ctx, "get_quota", `{}`, run); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedToolResult(ctx, "get_metrics", `{}`, run); err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatalf("tool names aliased: calls=%d, want 5", calls)
	}
}

func TestToolResultCacheWebSearchNormalization(t *testing.T) {
	ctx := withToolResultCache(context.Background())
	var calls int
	run := func() (string, error) {
		calls++
		return fmt.Sprintf(`{"ok":true,"call":%d}`, calls), nil
	}
	for _, args := range []string{
		`{"query":"  ESP32\t S3\n资料　"}`,
		`{"limit":3,"query":"ESP32 S3 资料"}`,
		`{"query":"ESP32 S3 资料","limit":0}`,
		`{"query":"ESP32 S3 资料","limit":null}`,
	} {
		got, err := cachedToolResult(ctx, "web_search", args, run)
		if err != nil || got != `{"ok":true,"call":1}` {
			t.Fatalf("equivalent search did not reuse: args=%s got=%s err=%v", args, got, err)
		}
	}
	for _, args := range []string{
		`{"query":"ESP32 S2 资料"}`,
		`{"query":"ESP32 S3 资料","limit":1}`,
	} {
		if _, err := cachedToolResult(ctx, "web_search", args, run); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("query/limit identity lost: calls=%d, want 3", calls)
	}
}

func TestToolResultCacheOversizedArgumentsCannotBypassValidation(t *testing.T) {
	for _, name := range []string{"get_metrics", "get_quota", "web_search"} {
		t.Run(name, func(t *testing.T) {
			ctx := withToolResultCache(context.Background())
			valid := `{}`
			if name == "web_search" {
				valid = `{"query":"test"}`
			}
			calls := 0
			wantErr := errors.New("tool arguments too large")
			invoke := func(args string) (string, error) {
				return cachedToolResult(ctx, name, args, func() (string, error) {
					calls++
					if len(args) > 4096 {
						return "", wantErr
					}
					return "validated success", nil
				})
			}
			if got, err := invoke(valid); err != nil || got != "validated success" {
				t.Fatalf("initial invocation got %q %v", got, err)
			}
			boundary := strings.Repeat(" ", 4096-len(valid)) + valid
			if got, err := invoke(boundary); err != nil || got != "validated success" || calls != 1 {
				t.Fatalf("4096-byte arguments failed to reuse: %q %v calls=%d", got, err, calls)
			}
			for range 2 {
				if got, err := invoke(" " + boundary); got != "" || err != wantErr {
					t.Fatalf("oversized args bypassed validation via cached result: %q %v", got, err)
				}
			}
			if got, err := invoke(valid); err != nil || got != "validated success" || calls != 3 {
				t.Fatalf("oversized invocation poisoned cache: %q %v calls=%d", got, err, calls)
			}
		})
	}
}

func TestToolResultCacheInvalidArgumentsNeverAliasValid(t *testing.T) {
	for _, name := range []string{"get_metrics", "get_quota", "web_search"} {
		t.Run(name, func(t *testing.T) {
			ctx := withToolResultCache(context.Background())
			var calls int
			run := func() (string, error) { calls++; return fmt.Sprint(calls), nil }
			valid := `{}`
			invalid := []string{"", " ", "null", "[]", "0", `"{}"`, "{", `{} {}`, `{} null`, `{} trailing`}
			if name == "web_search" {
				valid = `{"query":"test"}`
				invalid = append(invalid, `{}`, `{"query":null}`, `{"query":" \t"}`,
					`{"query":"test","limit":"3"}`, `{"query":"test","limit":3.0}`,
					`{"query":"test","limit":6}`, `{"query":"test","extra":true}`,
					`{"query":false,"query":"test"}`, `{"query":"test\u0000"}`,
					`{"query":"`+strings.Repeat("x", 257)+`"}`)
			}
			if _, err := cachedToolResult(ctx, name, valid, run); err != nil {
				t.Fatal(err)
			}
			for _, args := range invalid {
				for range 2 {
					before := calls
					if _, err := cachedToolResult(ctx, name, args, run); err != nil {
						t.Fatal(err)
					}
					if calls != before+1 {
						t.Fatalf("invalid args reused a cache entry: %q", args)
					}
				}
			}
			got, err := cachedToolResult(ctx, name, valid, run)
			if err != nil || got != "1" {
				t.Fatalf("invalid invocation poisoned valid result: %q %v", got, err)
			}
		})
	}
}

func TestToolResultCacheFailuresAreRetried(t *testing.T) {
	wantErr := errors.New("temporary failure")
	for _, test := range []struct {
		name   string
		result string
		err    error
	}{
		{"error", "partial", wantErr},
		{"canceled error", "", context.Canceled},
		{"deadline error", "", context.DeadlineExceeded},
		{"JSON failure", `{"ok":false,"message":"try again"}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := withToolResultCache(context.Background())
			calls := 0
			run := func() (string, error) {
				calls++
				if calls == 1 {
					return test.result, test.err
				}
				return `{"ok":true}`, nil
			}
			got, err := cachedToolResult(ctx, "get_metrics", `{}`, run)
			if got != test.result || err != test.err {
				t.Fatalf("failure changed: %q %v", got, err)
			}
			for range 2 {
				got, err = cachedToolResult(ctx, "get_metrics", `{}`, run)
				if got != `{"ok":true}` || err != nil {
					t.Fatalf("retry unsuccessful: %q %v", got, err)
				}
			}
			if calls != 2 {
				t.Fatalf("calls=%d, want failed run plus one successful retry", calls)
			}
		})
	}
}

func TestToolResultCacheOnlyExplicitReadOnlyAllowlist(t *testing.T) {
	ctx := withToolResultCache(context.Background())
	for _, name := range []string{
		"lock_computer", "set_page", "set_screen", "ha_get_state", "ha_turn_on",
		"memory_search", "memory_save", "memory_delete", "get_current_time", "unknown", "GET_METRICS",
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			for range 2 {
				_, err := cachedToolResult(ctx, name, `{}`, func() (string, error) {
					calls++
					return `{"ok":true}`, nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatalf("non-allowlisted tool cached: calls=%d", calls)
			}
		})
	}
}

// Signal exactly when a caller reaches the cancellation-aware wait, avoiding
// sleeps or scheduling assumptions in singleflight/cancellation tests.
type toolCacheWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *toolCacheWaitingContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func waitForToolCacheSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool cache test synchronization")
	}
}

func TestToolResultCacheConcurrentSingleflightDoesNotLockRun(t *testing.T) {
	ctx := withToolResultCache(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	run := func() (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "shared result", nil
	}
	type outcome struct {
		result string
		err    error
	}
	const waiters = 16
	results := make(chan outcome, waiters+1)
	invoke := func(ctx context.Context) {
		result, err := cachedToolResult(ctx, "get_metrics", `{}`, run)
		results <- outcome{result, err}
	}
	go invoke(ctx)
	waitForToolCacheSignal(t, started)
	for range waiters {
		waiter := &toolCacheWaitingContext{Context: ctx, waiting: make(chan struct{})}
		go invoke(waiter)
		waitForToolCacheSignal(t, waiter.waiting)
	}

	independent := make(chan struct{})
	go func() {
		defer close(independent)
		_, err := cachedToolResult(ctx, "get_quota", `{}`, func() (string, error) { return "quota", nil })
		if err != nil {
			t.Errorf("independent key: %v", err)
		}
	}()
	waitForToolCacheSignal(t, independent)
	close(release)
	for range waiters + 1 {
		select {
		case got := <-results:
			if got.err != nil || got.result != "shared result" {
				t.Fatalf("shared run returned %q %v", got.result, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("singleflight callers did not finish")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent run count=%d, want 1", calls.Load())
	}
}

func TestToolResultCacheWaiterCancellationDoesNotCancelOwner(t *testing.T) {
	ctx := withToolResultCache(context.Background())
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	run := func() (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return "completed", nil
	}
	go func() {
		defer close(finished)
		got, err := cachedToolResult(ctx, "get_metrics", `{}`, run)
		if err != nil || got != "completed" {
			t.Errorf("owner got %q %v", got, err)
		}
	}()
	waitForToolCacheSignal(t, started)
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	waiter := &toolCacheWaitingContext{Context: cancelCtx, waiting: make(chan struct{})}
	canceled := make(chan struct{})
	go func() {
		defer close(canceled)
		got, err := cachedToolResult(waiter, "get_metrics", `{}`, run)
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Errorf("waiter got %q %v, want cancellation", got, err)
		}
	}()
	waitForToolCacheSignal(t, waiter.waiting)
	cancel()
	waitForToolCacheSignal(t, canceled)
	close(release)
	waitForToolCacheSignal(t, finished)
	got, err := cachedToolResult(ctx, "get_metrics", `{}`, run)
	if err != nil || got != "completed" || calls.Load() != 1 {
		t.Fatalf("waiter cancellation poisoned success: %q %v calls=%d", got, err, calls.Load())
	}
	if got, err := cachedToolResult(cancelCtx, "get_metrics", `{}`, run); got != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller received cached success: %q %v", got, err)
	}
}

func TestToolResultCacheCanceledOwnerDoesNotPublishSuccess(t *testing.T) {
	ctx := withToolResultCache(context.Background())
	owner, cancel := context.WithCancel(ctx)
	defer cancel()
	calls := 0
	got, err := cachedToolResult(owner, "get_quota", `{}`, func() (string, error) {
		calls++
		cancel()
		return "stale success", nil
	})
	if got != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled owner got %q %v", got, err)
	}
	for range 2 {
		got, err = cachedToolResult(ctx, "get_quota", `{}`, func() (string, error) {
			calls++
			return "fresh success", nil
		})
		if got != "fresh success" || err != nil {
			t.Fatalf("retry after cancellation got %q %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want canceled run and successful retry", calls)
	}
}
