package brain

import "aemeath/agent/internal/replytext"

// Preserve the archive while preventing old malformed assistant replies from
// teaching the model to reproduce serialized tool calls. Never sanitize user text.
func historicalReply(text string) string {
	if replytext.Validate(text) != nil {
		return "（该历史回复包含异常工具协议，内容已隔离，不能视为已完成的回答或操作。）"
	}
	return text
}
