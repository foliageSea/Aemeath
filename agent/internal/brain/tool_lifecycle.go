package brain

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
)

// lifecycleTool prevents canceled or invalidated turns from starting tool work.
// It performs no intent check and makes no model requests.
type lifecycleTool struct {
	tool.InvokableTool
	engine *Engine
}

func (t lifecycleTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if scope, ok := ctx.Value(requestScopeKey{}).(requestScope); ok {
		t.engine.mu.Lock()
		current := t.engine.requestCurrentLocked(scope)
		t.engine.mu.Unlock()
		if !current {
			return "", context.Canceled
		}
	}
	return t.InvokableTool.InvokableRun(ctx, args, opts...)
}
