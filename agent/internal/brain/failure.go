package brain

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

// requestFailure contains only allowlisted diagnostics. It deliberately does not
// retain or unwrap provider errors, which can contain credentials or raw bodies.
type requestFailure struct {
	stage  string
	code   string
	status int
}

func (e *requestFailure) Error() string {
	stage := "模型／工具处理"
	switch e.stage {
	case "model":
		stage = "模型请求"
	case "tool_decision":
		stage = "工具决策"
	case "tool_execution":
		stage = "工具执行"
	case "final_reply":
		stage = "最终回复"
	}
	message := "请求失败，请检查模型与工具配置"
	switch e.code {
	case "authentication":
		message = "模型服务鉴权失败，请检查 API Key"
	case "permission":
		message = "模型服务拒绝访问，请检查权限"
	case "rate_limit":
		message = "模型服务限流或额度不足"
	case "model_not_found":
		message = "模型或接口不存在，请检查模型名称与地址"
	case "invalid_request":
		message = "模型服务拒绝请求，请检查模型、工具定义及参数兼容性"
	case "service_unavailable":
		message = "模型服务暂不可用"
	case "http_error":
		message = "模型服务返回 HTTP 错误"
	case "connection":
		message = "无法连接模型服务，请检查网络与服务地址"
	case "timeout":
		message = "请求超时"
	case "step_limit":
		message = "已达到工具处理步数上限"
	case "decision_truncated":
		message = "工具决策输出达到 token 上限，未执行该决策中的工具"
	case "invalid_decision":
		message = "模型未返回有效的工具决策"
	case "tool_execution":
		message = "工具调用失败，请查看本轮工具记录"
	case "stream_protocol":
		message = "模型流式响应不完整或格式不兼容"
	case "response_limit":
		message = "模型响应超过安全大小限制"
	case "tool_call_limit":
		message = "单次工具调用数量超过安全上限，未执行该批工具"
	case "invalid_response":
		message = "模型响应无效，请检查接口兼容性"
	case "empty_response":
		message = "模型未返回最终回复文字"
	}
	status := ""
	if e.status >= 100 && e.status <= 599 {
		status = fmt.Sprintf(" · HTTP %d", e.status)
	}
	return fmt.Sprintf("%s失败：%s [%s%s]", stage, message, e.code, status)
}

func httpFailure(status int) *requestFailure {
	code := "http_error"
	switch {
	case status == 401:
		code = "authentication"
	case status == 403:
		code = "permission"
	case status == 404:
		code = "model_not_found"
	case status == 408 || status == 504:
		code = "timeout"
	case status == 429:
		code = "rate_limit"
	case status >= 500:
		code = "service_unavailable"
	case status >= 400:
		code = "invalid_request"
	}
	return &requestFailure{code: code, status: status}
}

// Only the agent-facing wrapper classifies tool failures; direct tool callers
// and tool observers keep their existing error/cancellation semantics.
type diagnosticTool struct {
	tool.InvokableTool
}

func (t diagnosticTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	out, err := t.InvokableTool.InvokableRun(ctx, args, opts...)
	return out, safeRequestFailure(err, "tool_execution")
}

// Classify by types and local sentinels, never by provider-controlled text.
func safeRequestFailure(err error, stage string) error {
	if err == nil {
		return nil
	}
	var failure *requestFailure
	if errors.As(err, &failure) {
		copy := *failure
		if copy.stage == "" {
			copy.stage = stage
		}
		return &copy
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, compose.ErrExceedMaxSteps) {
		return &requestFailure{stage: "agent", code: "step_limit"}
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode > 0 {
		failure = httpFailure(apiErr.HTTPStatusCode)
		failure.stage = stage
		return failure
	}
	var networkErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkErr) && networkErr.Timeout() {
		return &requestFailure{stage: stage, code: "timeout"}
	}
	code := "invalid_response"
	if stage == "tool_execution" {
		code = "tool_execution"
	} else if stage == "agent" {
		code = "request_failed"
	}
	return &requestFailure{stage: stage, code: code}
}
