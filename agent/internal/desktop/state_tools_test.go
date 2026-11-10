package desktop

import (
	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/voice"
	"testing"
)

func TestSnapshotOwnsVoiceToolCalls(t *testing.T) {
	service := &Service{state: State{Voice: voice.State{ToolCalls: []brain.ToolCall{{ID: 1, Name: "get_metrics", Status: "running"}}}}}
	first := service.Snapshot()
	first.Voice.ToolCalls[0].Status = "consumer edit"
	if service.Snapshot().Voice.ToolCalls[0].Status != "running" {
		t.Fatal("snapshot tool slice aliases service state")
	}
	service.mu.Lock()
	service.state.Voice.ToolCalls[0].Status = "succeeded"
	service.mu.Unlock()
	if first.Voice.ToolCalls[0].Status != "consumer edit" {
		t.Fatal("service update mutated retained snapshot")
	}
	clone := service.Snapshot().Clone()
	clone.Voice.ToolCalls[0].Name = "changed"
	if service.Snapshot().Voice.ToolCalls[0].Name != "get_metrics" {
		t.Fatal("Clone tool slice aliases state")
	}
}
