package brain

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// finalStreamModel separates tool selection from speech. Tool-enabled responses
// are never streamed to the caller: even OpenAI-compatible servers can emit
// content before tool_calls. ReAct runs those completed calls exactly once.
// Once selection ends, a separate model with NO bound tools streams the final
// answer using the accumulated tool results. This costs one extra text request,
// but avoids guessing whether an unfinished tool-enabled stream is final.
// Generate remains unchanged so Reply preserves its existing behavior.
type finalStreamModel struct {
	base     model.ToolCallingChatModel
	decision model.ToolCallingChatModel
}

func (m *finalStreamModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.base.WithTools(tools)
	if err != nil {
		return nil, safeRequestFailure(err, "tool_decision")
	}
	return &finalStreamModel{base: m.base, decision: bound}, nil
}

func (m *finalStreamModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	out, err := m.decision.Generate(ctx, input, opts...)
	return out, safeRequestFailure(err, "tool_decision")
}

func (m *finalStreamModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	// The selector does not write a completed answer that would be discarded.
	// Its instruction is private to this request; final generation receives the
	// original conversation and exact accumulated tool results instead.
	selection := append([]*schema.Message(nil), input...)
	selection = append(selection, schema.SystemMessage("本轮只判断是否还需要调用工具，不要生成最终回答。常识问答或闲聊若已有知识足够回答，直接只输出 READY，无需先调用任何工具。所有工具（包括 web_search、设备工具和 Home Assistant 工具）仅在与用户当前请求直接相关且确有必要时调用，不要为了使用工具而调用。设备或 Home Assistant 控制必须有用户当前明确请求；历史已完成操作不代表当前授权，不要重放。结合当前请求、本轮时间快照与已有工具结果判断；已有充分结果时不要重复调用，直接只输出 READY。时间、日期和星期直接依据本轮时间快照，不要为确认当前时间而搜索。确需最新外部资料的实时搜索任务仍须实际调用 web_search，不能只输出计划。例如查询今年年底和明年初的最新安排，直接依据快照确定年份，再搜索对应年份的资料，拿到充分结果后才结束。确需工具时必须返回实际工具调用，不能只说“我先查一下”或描述计划；不要输出解释、旁白或用户回复。已有知识或结果足够回答，或工具已明确失败且需要向用户说明时，只输出 READY。"))
	opts = append(opts, model.WithMaxTokens(512))
	var selected *schema.Message
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		selected, err = m.decision.Generate(ctx, selection, opts...)
		if err != nil {
			return nil, safeRequestFailure(err, "tool_decision")
		}
		if selected == nil {
			return nil, &requestFailure{stage: "tool_decision", code: "invalid_decision"}
		}
		if selected.ResponseMeta != nil && selected.ResponseMeta.FinishReason == "length" {
			return nil, &requestFailure{stage: "tool_decision", code: "decision_truncated"}
		}
		if len(selected.ToolCalls) > 0 || strings.EqualFold(strings.TrimSpace(selected.Content), "READY") {
			break
		}
		// A plan or ordinary text is not evidence that tool work is complete.
		// Repair only this invalid decision, never replay the graph or its tools.
		if attempt == 1 {
			return nil, &requestFailure{stage: "tool_decision", code: "invalid_decision"}
		}
		selection = append(selection, selected, schema.SystemMessage("上一条没有返回有效工具决策。常识问答或闲聊若已有知识足够回答，直接只输出 READY，无需先调用任何工具。所有工具仅在与用户当前请求直接相关且确有必要时调用；设备或 Home Assistant 控制必须有用户当前明确请求，历史已完成操作不代表当前授权。已有充分结果时不要重复调用，直接只输出 READY；工具明确失败且需要向用户说明时也只输出 READY。确需最新外部资料的实时搜索任务仍须返回实际工具调用，不能只输出计划；计划和承诺不代表任务完成。不要生成最终回答。"))
	}
	if len(selected.ToolCalls) > 0 {
		// Preserve the exact assistant tool-call message for model continuation
		// (reasoning providers may require its private reasoning metadata). ReAct
		// routes it to tools; none of this round reaches the speech callback.
		return schema.StreamReaderFromArray([]*schema.Message{selected}), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, safeRequestFailure(err, "final_reply")
	}
	// Use the unbound base model and explicitly forbid tool calls. In particular,
	// this is never a retry of the ReAct graph when streaming is unsupported.
	sr, err := m.base.Stream(ctx, input, model.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil {
		return nil, safeRequestFailure(err, "final_reply")
	}
	return schema.StreamReaderWithConvert(sr, func(chunk *schema.Message) (*schema.Message, error) {
		// Reject a provider violating tool_choice BEFORE ReAct can route it to
		// the tools node. The final request may never execute another tool.
		if chunk != nil && (len(chunk.ToolCalls) > 0 || chunk.ToolCallID != "" || (chunk.Role != "" && chunk.Role != schema.Assistant)) {
			return nil, &requestFailure{stage: "final_reply", code: "stream_protocol"}
		}
		return chunk, nil
	}), nil
}

type deltaError struct{ err error }

func (e *deltaError) Error() string { return "brain: delta callback failed" }
func (e *deltaError) Unwrap() error { return e.err }

func (e *Engine) streamReply(ctx context.Context, input []*schema.Message, onDelta func(string) error) (*schema.Message, error) {
	var sr *schema.StreamReader[*schema.Message]
	var err error
	if e.agent != nil {
		sr, err = e.agent.Stream(ctx, input)
	} else {
		sr, err = e.model.Stream(ctx, input, model.WithToolChoice(schema.ToolChoiceForbidden))
	}
	if err != nil {
		return nil, safeRequestFailure(err, "final_reply")
	}
	defer sr.Close()
	var content strings.Builder
	finished := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, safeRequestFailure(err, "final_reply")
		}
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, safeRequestFailure(err, "final_reply")
		}
		if err := ctx.Err(); err != nil {
			return nil, safeRequestFailure(err, "final_reply")
		}
		if chunk == nil {
			continue
		}
		if (chunk.Role != "" && chunk.Role != schema.Assistant) || len(chunk.ToolCalls) > 0 || chunk.ToolCallID != "" {
			return nil, &requestFailure{stage: "final_reply", code: "stream_protocol"}
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.FinishReason != "" {
			if chunk.ResponseMeta.FinishReason != "stop" {
				return nil, &requestFailure{stage: "final_reply", code: "stream_protocol"}
			}
			finished = true
		}
		// Never use Message.String, ReasoningContent, Extra or tool argument fields.
		if chunk.Content == "" {
			continue
		}
		if content.Len()+len(chunk.Content) > maxTextBytes {
			return nil, &requestFailure{stage: "final_reply", code: "response_limit"}
		}
		content.WriteString(chunk.Content)
		if onDelta != nil {
			if err := onDelta(chunk.Content); err != nil {
				return nil, &deltaError{err: err}
			}
		}
	}
	if !finished {
		return nil, &requestFailure{stage: "final_reply", code: "stream_protocol"}
	}
	return schema.AssistantMessage(content.String(), nil), nil
}

// Bound bytes without turning a network stream into an in-memory response.
// An overrun must be an error, not EOF, so truncated output cannot enter history.
type boundedStreamBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedStreamBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, &requestFailure{code: "response_limit"}
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:int(b.remaining)]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
