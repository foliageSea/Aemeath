package voice

import (
	"strings"
	"testing"
)

func TestTavilyKeyIsOptionalTrimmedValidatedAndRedacted(t *testing.T) {
	cfg, err := NormalizeConfig(Config{TavilyAPIKey: "  fixture-tavily-key  "})
	if err != nil || cfg.TavilyAPIKey != "fixture-tavily-key" {
		t.Fatal("Tavily key was not normalized")
	}
	if _, err := NormalizeConfig(Config{}); err != nil {
		t.Fatal("missing Tavily key blocks other voice features")
	}
	for _, key := range []string{strings.Repeat("x", 4097), "fixture" + string(rune(10)) + "injected"} {
		if _, err := NormalizeConfig(Config{TavilyAPIKey: key}); err == nil {
			t.Fatal("invalid Tavily key accepted")
		}
	}
	session := &session{cfg: cfg}
	if text := session.safe("request failed " + cfg.TavilyAPIKey); strings.Contains(text, cfg.TavilyAPIKey) {
		t.Fatal("Tavily credential escaped runtime redaction")
	}
}
