package voice

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFishConfigurationDefaultsAndJSONReferenceID(t *testing.T) {
	cfg, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TTSProvider != "mimo" || cfg.FishBaseURL != "https://api.fish.audio" || cfg.FishModel != "s2.1-pro" || cfg.FishLatency != "balanced" {
		t.Fatalf("wrong provider defaults: %+v", cfg)
	}
	if cfg.MiMoBaseURL != "https://api.xiaomimimo.com/v1" {
		t.Fatal("existing recognition default changed")
	}
	var input Config
	if err := json.Unmarshal([]byte(`{"enabled":true,"ttsProvider":" FISH ","fishReferenceID":" voice-123 ","fishAPIKey":" key ","fishBaseURL":"https://fish.example/","fishModel":" custom-model ","fishLatency":" NORMAL "}`), &input); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.TTSProvider != "fish" || got.FishReferenceID != "voice-123" || got.FishAPIKey != "key" || got.FishBaseURL != "https://fish.example" || got.FishModel != "custom-model" || got.FishLatency != "normal" {
		t.Fatal("Fish configuration did not normalize")
	}
	// A saved reference_id needs no inline transcript or installed sample at config time.
	if got.FishReferenceText != "" {
		t.Fatal("reference_id unexpectedly requires inline reference text")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"fishReferenceID":"voice-123"`) {
		t.Fatalf("reference_id JSON spelling changed: %s", b)
	}
}

func TestFishConfigurationValidationDoesNotExposeValues(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Config)
	}{
		{"provider", func(c *Config) { c.TTSProvider = "automatic-secret" }},
		{"latency", func(c *Config) { c.FishLatency = "bad-secret" }},
		{"URL credentials", func(c *Config) { c.FishBaseURL = "https://user:secret@example.test" }},
		{"URL query", func(c *Config) { c.FishBaseURL = "https://example.test?key=secret" }},
		{"URL fragment", func(c *Config) { c.FishBaseURL = "https://example.test/#secret" }},
		{"URL scheme", func(c *Config) { c.FishBaseURL = "file:///secret" }},
		{"model header", func(c *Config) { c.FishModel = "model\r\nsecret" }},
		{"reference ID", func(c *Config) { c.FishReferenceID = strings.Repeat("secret", 100) }},
		{"transcript size", func(c *Config) { c.FishReferenceText = strings.Repeat("secret", 3000) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Enabled: true, TTSProvider: "fish"}
			tc.set(&cfg)
			if _, err := NormalizeConfig(cfg); err == nil {
				t.Fatal("invalid Fish config accepted")
			} else if strings.Contains(err.Error(), "secret") {
				t.Fatalf("validation exposed value: %v", err)
			}
		})
	}
	// Missing credentials/assets remain diagnostic-time errors, allowing manual audio checks.
	if _, err := NormalizeConfig(Config{Enabled: true, TTSProvider: "fish", FishReferenceText: "第一行\n第二行"}); err != nil {
		t.Fatal(err)
	}
}
