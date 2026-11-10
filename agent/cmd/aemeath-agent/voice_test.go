package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVoiceConfigurationEnvironmentAndStrictJSON(t *testing.T) {
	t.Setenv("AEMEATH_LLM_API_KEY", "from-environment")
	t.Setenv("AEMEATH_LLM_BASE_URL", "https://example.test/v1")
	t.Setenv("AEMEATH_LLM_MODEL", "model")
	t.Setenv("MIMO_API_KEY", "mimo-environment")
	t.Setenv("TAVILY_API_KEY", "tavily-from-environment")
	path := filepath.Join(t.TempDir(), "voice.json")
	if err := os.WriteFile(path, []byte(`{"llmAPIKey":"from-file","tavilyAPIKey":"tavily-from-file","volume":70,"llmTools":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadVoiceConfig(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.LLMAPIKey != "from-environment" || cfg.MiMoAPIKey != "mimo-environment" || cfg.TavilyAPIKey != "tavily-from-environment" || cfg.LLMModel != "model" || cfg.Volume != 70 || !cfg.LLMTools {
		t.Fatal("configuration precedence failed")
	}
	for _, bad := range []string{`{"llmApiKy":"typo"}`, `{} {}`, strings.Repeat(" ", 1<<20+1)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadVoiceConfig(path, false); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
