package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReasoningEffortConfigAndProductionRequest(t *testing.T) {
	var input Config
	if err := json.Unmarshal([]byte(`{"llmModel":"mock","reasoningEffort":" HIGH "}`), &input); err != nil {
		t.Fatal(err)
	}
	cfg, err := NormalizeConfig(input)
	if err != nil || cfg.ReasoningEffort != "high" {
		t.Fatalf("normalize: %q %v", cfg.ReasoningEffort, err)
	}
	data, err := json.Marshal(cfg)
	if err != nil || !strings.Contains(string(data), `"reasoningEffort":"high"`) {
		t.Fatalf("JSON: %s %v", data, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["reasoning_effort"] != "high" {
			t.Errorf("effort not forwarded: %v", request["reasoning_effort"])
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "你好"}}}})
	}))
	defer server.Close()
	cfg.LLMBaseURL = server.URL
	engine, err := productionDependencies().newBrain(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.(interface{ Close() error }).Close()
	if _, err := engine.Reply(context.Background(), "你好"); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeConfig(Config{ReasoningEffort: "secret-invalid"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid effort: %v", err)
	}
}
