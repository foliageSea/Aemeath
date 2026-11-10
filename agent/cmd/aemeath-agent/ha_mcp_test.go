package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aemeath/agent/internal/voice"
)

func TestHAMCPJSONAndEnvironmentPrecedence(t *testing.T) {
	t.Setenv("HA_MCP_ENABLED", "")
	t.Setenv("HA_MCP_URL", "")
	t.Setenv("HA_ACCESS_TOKEN", "")
	path := filepath.Join(t.TempDir(), "voice.json")
	data, err := json.Marshal(voice.Config{HAMCPEnabled: true, HAMCPURL: "http://file-ha.local:8123", HAAccessToken: "file-token", Volume: 70})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadVoiceConfig(path, false)
	if err != nil || !cfg.HAMCPEnabled || cfg.HAMCPURL != "http://file-ha.local:8123/api/mcp/assist" || cfg.HAAccessToken != "file-token" {
		t.Fatal("JSON HA settings did not load")
	}
	t.Setenv("HA_MCP_ENABLED", "false")
	t.Setenv("HA_MCP_URL", "https://environment-ha.test/api/mcp/assist")
	t.Setenv("HA_ACCESS_TOKEN", "environment-token")
	cfg, err = loadVoiceConfig(path, false)
	if err != nil || cfg.HAMCPEnabled || cfg.HAMCPURL != "https://environment-ha.test/api/mcp/assist" || cfg.HAAccessToken != "environment-token" {
		t.Fatal("HA environment did not override JSON including false")
	}
	t.Setenv("HA_MCP_ENABLED", "true")
	cfg, err = loadVoiceConfig(path, false)
	if err != nil || !cfg.HAMCPEnabled {
		t.Fatal("HA environment true was not applied")
	}
}

func TestHAMCPEnvironmentRejectsInvalidBooleanWithoutEchoingValue(t *testing.T) {
	t.Setenv("HA_MCP_ENABLED", "fixture-secret-invalid-flag")
	_, err := loadVoiceConfig("", false)
	if err == nil || !strings.Contains(err.Error(), "HA_MCP_ENABLED") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("invalid HA enabled flag was accepted or leaked")
	}
}
