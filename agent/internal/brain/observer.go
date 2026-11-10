package brain

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ToolCall is a bounded display event, separate from model and speech content.
// Millis is elapsed execution time, at least 1 for terminal events and 0 while running.
type ToolCall struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
	Status    string `json:"status"`
	Millis    int64  `json:"millis"`
}

type toolObserverKey struct{}
type toolObserver struct {
	mu      sync.Mutex
	next    uint64
	observe func(ToolCall)
}

// WithToolObserver starts an independent ID sequence for this context and its
// descendants. Callbacks are synchronous and serialized; they should return
// promptly and must not recursively invoke tools using this same context.
func WithToolObserver(ctx context.Context, observer func(ToolCall)) context.Context {
	return context.WithValue(ctx, toolObserverKey{}, &toolObserver{observe: observer})
}

func startTool(ctx context.Context, name, args string) func(string, error) {
	observer, _ := ctx.Value(toolObserverKey{}).(*toolObserver)
	if observer == nil || observer.observe == nil {
		return func(string, error) {}
	}
	event := ToolCall{Name: name, Arguments: toolArguments(name, args), Status: "running"}
	observer.mu.Lock()
	observer.next++
	event.ID = observer.next
	observer.observe(event)
	observer.mu.Unlock()
	started := time.Now()
	return func(result string, err error) {
		event.Millis = max(1, time.Since(started).Milliseconds())
		event.Status = "succeeded"
		// Display only fixed known summaries. Never expose raw provider/host errors,
		// quota/metrics payloads, unknown argument fields or invalid JSON.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			event.Status, event.Result = "canceled", "工具调用已取消"
		} else if err != nil {
			event.Status, event.Result = "failed", "工具调用失败"
		} else {
			var outcome struct {
				OK *bool `json:"ok"`
			}
			if json.Unmarshal([]byte(result), &outcome) == nil && outcome.OK != nil && !*outcome.OK {
				event.Status, event.Result = "failed", "操作未成功，请检查系统支持和权限"
				if name == "lock_computer" {
					event.Result = "锁屏请求未能提交，请检查系统支持和权限；电脑锁屏状态尚未确认"
				}
			} else {
				switch name {
				case "get_metrics":
					event.Result = "已读取系统指标"
				case "get_quota":
					event.Result = "已读取额度和用量"
				case "set_page":
					event.Result = "切页指令已发送，尚未确认设备实际页面"
				case "set_screen":
					event.Result = "屏幕设置指令已发送，最终状态以设备上报为准"
				case "lock_computer":
					event.Result = "已提交锁屏请求，尚未确认最终锁屏状态"
				default:
					event.Result = "工具调用完成"
				}
			}
		}
		observer.mu.Lock()
		defer observer.mu.Unlock()
		observer.observe(event)
	}
}

func toolArguments(name, args string) string {
	if len(args) > 4096 {
		return ""
	}
	switch name {
	case "lock_computer", "get_metrics", "get_quota":
		return "{}"
	case "set_page":
		var input struct {
			Page string `json:"page"`
		}
		if decodeArgs(args, &input) == nil {
			switch input.Page {
			case "metrics", "quota", "voice":
				data, _ := json.Marshal(input)
				return string(data)
			}
		}
	case "set_screen":
		var input struct {
			On         *bool `json:"on"`
			Brightness *int  `json:"brightness"`
		}
		if decodeArgs(args, &input) == nil && input.On != nil && input.Brightness != nil && *input.Brightness >= 0 && *input.Brightness <= 100 {
			data, _ := json.Marshal(input)
			return string(data)
		}
	}
	return ""
}
