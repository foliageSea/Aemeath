package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceShortcutSettingsMigrationNormalizationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadSettings(path)
	if err != nil || cfg.VoiceShortcut != "" {
		t.Fatalf("migration: %q %v", cfg.VoiceShortcut, err)
	}
	cfg.VoiceShortcut = " shift + command + a "
	cfg, err = normalizeSettings(cfg)
	if err != nil || cfg.VoiceShortcut != "Shift+Super+A" {
		t.Fatalf("normalize: %q %v", cfg.VoiceShortcut, err)
	}
	if err := persistSettings(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSettings(path)
	if err != nil || loaded.VoiceShortcut != cfg.VoiceShortcut {
		t.Fatalf("round trip: %q %v", loaded.VoiceShortcut, err)
	}
	for _, invalid := range []string{"A", "Shift+Space", "Ctrl+Ctrl+F8", "Ctrl+"} {
		cfg.VoiceShortcut = invalid
		if _, err := normalizeSettings(cfg); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}
