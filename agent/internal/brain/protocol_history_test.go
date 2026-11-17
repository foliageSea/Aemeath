package brain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestProtocolHistoryIsolatedWithoutMutatingArchive(t *testing.T) {
	bad := `<tool_search><query>private-query</query></tool_search>`
	user := "请解释 <tool_call> 的用途"
	history := []*schema.Message{schema.UserMessage(user), schema.AssistantMessage(bad, nil), schema.UserMessage("正常问题"), schema.AssistantMessage("正常答案", nil)}
	for _, summary := range []string{"正常摘要", bad} {
		input := contextMessages("persona", summary, history, user)
		if len(input) != 7 {
			t.Fatalf("messages=%d", len(input))
		}
		if input[2].Content != user || input[6].Content != user {
			t.Fatal("user text was sanitized")
		}
		if strings.Contains(input[3].Content, "private-query") || input[3] == history[1] {
			t.Fatal("bad assistant was not isolated in copy")
		}
		if input[5].Content != "正常答案" {
			t.Fatal("safe answer changed")
		}
		if strings.Contains(input[1].Content, "private-query") {
			t.Fatal("bad summary escaped")
		}

		compression := summaryInput(summary, history)
		var payload struct {
			Summary string       `json:"previous_summary"`
			Turns   []memoryTurn `json:"older_turns"`
		}
		if err := json.Unmarshal([]byte(compression[1].Content), &payload); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(payload.Summary, "private-query") || strings.Contains(payload.Turns[0].Assistant, "private-query") {
			t.Fatal("protocol entered compression input")
		}
		if payload.Turns[0].User != user || payload.Turns[1].Assistant != "正常答案" {
			t.Fatal("safe history changed")
		}
	}
	if history[1].Content != bad || history[0].Content != user {
		t.Fatal("original archive mutated")
	}
}
