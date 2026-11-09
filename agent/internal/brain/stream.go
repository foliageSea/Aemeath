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
		return nil, err
	}
	return &finalStreamModel{base: m.base, decision: bound}, nil
}

func (m *finalStreamModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.decision.Generate(ctx, input, opts...)
}

func (m *finalStreamModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	// The selector does not write a completed answer that would be discarded.
	// Its instruction is private to this request; final generation receives the
	// original conversation and exact accumulated tool results instead.
	selection := append([]*schema.Message(nil), input...)
	selection = append(selection, schema.SystemMessage("本轮只判断是否还需要调用设备工具。需要时只返回工具调用，不输出解释、旁白或用户回复；已具备回答信息时不调用工具，只输出 READY。不要生成最终回答。"))
	opts = append(opts, model.WithMaxTokens(512))
	selected, err := m.decision.Generate(ctx, selection, opts...)
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, errors.New("missing tool decision")
	}
	if selected.ResponseMeta != nil && selected.ResponseMeta.FinishReason == "length" {
		return nil, errors.New("incomplete tool decision")
	}
	if len(selected.ToolCalls) > 0 {
		// Preserve the exact assistant tool-call message for model continuation
		// (reasoning providers may require its private reasoning metadata). ReAct
		// routes it to tools; none of this round reaches the speech callback.
		return schema.StreamReaderFromArray([]*schema.Message{selected}), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Use the unbound base model and explicitly forbid tool calls. In particular,
	// this is never a retry of the ReAct graph when streaming is unsupported.
	sr, err := m.base.Stream(ctx, input, model.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderWithConvert(sr, func(chunk *schema.Message) (*schema.Message, error) {
		// Reject a provider violating tool_choice BEFORE ReAct can route it to
		// the tools node. The final request may never execute another tool.
		if chunk != nil && (len(chunk.ToolCalls) > 0 || chunk.ToolCallID != "" || (chunk.Role != "" && chunk.Role != schema.Assistant)) {
			return nil, errors.New("unexpected tool in final answer stream")
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
		return nil, err
	}
	defer sr.Close()
	var content strings.Builder
	finished := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if chunk == nil {
			continue
		}
		if (chunk.Role != "" && chunk.Role != schema.Assistant) || len(chunk.ToolCalls) > 0 || chunk.ToolCallID != "" {
			return nil, errors.New("unexpected non-final assistant stream")
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.FinishReason != "" {
			if chunk.ResponseMeta.FinishReason != "stop" {
				return nil, errors.New("incomplete assistant stream")
			}
			finished = true
		}
		// Never use Message.String, ReasoningContent, Extra or tool argument fields.
		if chunk.Content == "" {
			continue
		}
		if content.Len()+len(chunk.Content) > maxTextBytes {
			return nil, errors.New("reply exceeds limit")
		}
		content.WriteString(chunk.Content)
		if onDelta != nil {
			if err := onDelta(chunk.Content); err != nil {
				return nil, &deltaError{err: err}
			}
		}
	}
	if !finished {
		return nil, errors.New("assistant stream ended without stop")
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
			return 0, errors.New("model stream exceeds limit")
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
