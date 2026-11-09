package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFishVoiceConfigurationEnvironmentOverridesJSON(t *testing.T) {
	overrides := map[string]string{
		"AEMEATH_TTS_PROVIDER": "fish", "FISH_BASE_URL": "https://fish-env.example/", "FISH_API_KEY": "fixture-env-key", "FISH_MODEL": "s2.1-pro", "FISH_REFERENCE_ID": "env-voice-id", "FISH_REFERENCE_TEXT": "环境中的逐字转写。", "FISH_LATENCY": "low",
	}
	for name, value := range overrides {
		t.Setenv(name, value)
	}
	path := filepath.Join(t.TempDir(), "voice.json")
	if err := os.WriteFile(path, []byte(`{"ttsProvider":"mimo","fishBaseURL":"https://fish-file.example","fishAPIKey":"fixture-file-key","fishModel":"file-model","fishReferenceID":"file-voice-id","fishReferenceText":"文件中的转写。","fishLatency":"normal"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadVoiceConfig(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TTSProvider != "fish" || cfg.FishBaseURL != "https://fish-env.example" || cfg.FishAPIKey != "fixture-env-key" || cfg.FishModel != "s2.1-pro" || cfg.FishReferenceID != "env-voice-id" || cfg.FishReferenceText != "环境中的逐字转写。" || cfg.FishLatency != "low" {
		t.Fatal("Fish environment overrides did not win over JSON")
	}
	for name := range overrides {
		t.Setenv(name, "")
	}
	file, err := loadVoiceConfig(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if file.TTSProvider != "mimo" || file.FishReferenceID != "file-voice-id" || file.FishAPIKey != "fixture-file-key" || file.FishLatency != "normal" {
		t.Fatal("Fish JSON fields did not load without overrides")
	}
	defaults, err := loadVoiceConfig("", true)
	if err != nil || defaults.TTSProvider != "mimo" || defaults.FishModel != "s2.1-pro" || defaults.FishLatency != "balanced" {
		t.Fatal("existing CLI provider defaults changed")
	}
}
