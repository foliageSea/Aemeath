package brain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// ToolCall is a bounded display event, separate from model and speech content.
// Millis is elapsed execution time, at least 1 for terminal events and 0 while running.
type ToolCall struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
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

func startTool(ctx context.Context, name, args string, title ...string) func(string, error) {
	observer, _ := ctx.Value(toolObserverKey{}).(*toolObserver)
	if observer == nil || observer.observe == nil {
		return func(string, error) {}
	}
	event := ToolCall{Name: name, Arguments: toolArguments(name, args), Status: "running"}
	if len(title) > 0 {
		event.Title = haSafe(title[0], "", 128)
	}
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
				if name == "get_current_time" {
					if report, ok := ParseCurrentTimeReport(result); ok {
						event.Result = report.Message
					}
				}
				if name == "web_search" {
					if report, ok := ParseWebSearchReport(result); ok {
						event.Result = report.Message
					}
				}
				if name == "lock_computer" {
					event.Result = "锁屏请求未能提交，请检查系统支持和权限；电脑锁屏状态尚未确认"
				}
			} else {
				switch name {
				case "get_current_time":
					if report, ok := ParseCurrentTimeReport(result); ok && report.OK {
						data, _ := json.Marshal(report)
						event.Result = string(data)
					} else {
						event.Status, event.Result = "failed", "时间结果无效，请重新查询"
					}
				case "web_search":
					if report, ok := ParseWebSearchReport(result); ok && report.OK {
						data, _ := json.Marshal(report)
						event.Result = string(data)
					} else {
						event.Status, event.Result = "failed", "搜索结果无效，未获得可引用的来源"
					}
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
		if strings.HasPrefix(name, "ha_") && err == nil && ctx.Err() == nil {
			if report, ok := ParseHomeAssistantReport(result); ok {
				event.Result = haReport(report)
				if !report.OK {
					event.Status = "failed"
				}
			} else {
				event.Status, event.Result = "failed", "Home Assistant 结果无效"
			}
		}
		if isMemoryTool(name) && err == nil && ctx.Err() == nil {
			var outcome struct {
				OK *bool `json:"ok"`
			}
			if len(result) > maxTextBytes || json.Unmarshal([]byte(result), &outcome) != nil || outcome.OK == nil || !*outcome.OK {
				event.Status, event.Result = "failed", "长期记忆操作未成功"
			} else {
				switch name {
				case "memory_search":
					event.Result = "长期记忆查询完成"
				case "memory_save":
					event.Result = "长期记忆已保存"
				case "memory_forget":
					event.Result = "长期记忆已删除"
				}
			}
		}
		observer.mu.Lock()
		defer observer.mu.Unlock()
		observer.observe(event)
	}
}

func isMemoryTool(name string) bool {
	return name == "memory_search" || name == "memory_save" || name == "memory_forget"
}

func toolArguments(name, args string) string {
	// Memory arguments can contain private facts or credentials, including in
	// rejected calls. Display no user-provided memory fields.
	if isMemoryTool(name) {
		return "{}"
	}
	if len(args) > 4096 {
		return ""
	}
	if strings.HasPrefix(name, "ha_") {
		return haSafeArguments(args, "")
	}
	switch name {
	case "get_current_time":
		var input struct {
			Timezone string `json:"timezone,omitempty"`
		}
		if decodeArgs(args, &input) == nil && len(input.Timezone) <= 128 {
			input.Timezone = normalizeTimeZone(input.Timezone)
			if input.Timezone == "" || input.Timezone == "Local" {
				return "{}"
			}
			if _, err := time.LoadLocation(input.Timezone); err == nil {
				data, _ := json.Marshal(input)
				return string(data)
			}
		}
	case "web_search":
		var input struct {
			Query string `json:"query"`
			Limit int    `json:"limit,omitempty"`
		}
		if decodeArgs(args, &input) == nil && input.Query != "" && len(input.Query) <= 1024 && input.Limit >= 0 && input.Limit <= 5 {
			data, _ := json.Marshal(input)
			return string(data)
		}
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
