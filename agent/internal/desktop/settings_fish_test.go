package desktop

import (
	"os"
	"path/filepath"
	"testing"

	"aemeath/agent/internal/voice"
)

func TestFishSettingsPersistAndReloadReferenceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	cfg := voice.Config{Enabled: true, TTSProvider: "fish", FishBaseURL: "https://fish.example", FishAPIKey: "fixture-fish-key", FishModel: "s2.1-pro", FishReferenceID: "saved-voice-id", FishLatency: "low", Volume: 70}
	settings, err := normalizeSettings(Settings{Voice: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Voice != settings.Voice {
		t.Fatal("saved Fish provider/key/reference configuration did not round-trip")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("settings file grants other users access: %v", info.Mode())
	}
	reloaded.Voice.FishReferenceID = ""
	reloaded.Voice.ReferenceAudio = "/fixtures/reference.wav"
	reloaded.Voice.FishReferenceText = "这是参考音频的完整转写。"
	if err := persistSettings(path, reloaded); err != nil {
		t.Fatal(err)
	}
	inline, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if inline.Voice.FishReferenceID != "" || inline.Voice.FishReferenceText != reloaded.Voice.FishReferenceText || inline.Voice.ReferenceAudio != reloaded.Voice.ReferenceAudio {
		t.Fatal("inline reference alternative did not round-trip")
	}
}

func TestExistingMiMoSettingsMigrateWithFishDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"voice":{"enabled":true,"mimoAPIKey":"fixture-mimo-key","referenceAudio":"/fixtures/old.wav","volume":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Voice.TTSProvider != "mimo" || got.Voice.MiMoAPIKey != "fixture-mimo-key" || got.Voice.ReferenceAudio != "/fixtures/old.wav" || got.Voice.Volume != 0 || got.Voice.FishModel != "s2.1-pro" {
		t.Fatal("existing MiMo settings changed during provider migration")
	}
}
