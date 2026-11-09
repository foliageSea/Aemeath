// Package voice owns one duplex voice session with a device. The caller owns retries.
package voice

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Enabled        bool    `json:"enabled"`
	LLMBaseURL     string  `json:"llmBaseURL"`
	LLMAPIKey      string  `json:"llmAPIKey"`
	LLMModel       string  `json:"llmModel"`
	LLMTools       bool    `json:"llmTools"`
	MiMoBaseURL    string  `json:"mimoBaseURL"`
	MiMoAPIKey     string  `json:"mimoAPIKey"`
	ReferenceAudio string  `json:"referenceAudio"`
	WakeModelDir   string  `json:"wakeModelDir"`
	WakeThreshold  float64 `json:"wakeThreshold"`
	Volume         int     `json:"volume"`
	Persona        string  `json:"persona"`
}

type State struct {
	Connected   bool   `json:"connected"`
	WakeReady   bool   `json:"wakeReady"`
	Phase       string `json:"phase"`
	Error       string `json:"error"`
	Notice      string `json:"notice,omitempty"`
	Transcript  string `json:"transcript"`
	Response    string `json:"response"`
	Turn        uint32 `json:"turn"`
	ASRMillis   int64  `json:"asrMillis"`
	AgentMillis int64  `json:"agentMillis"`
	TTSMillis   int64  `json:"ttsMillis"`
}

type Command struct {
	Kind string
	Text string
}
type Host interface {
	Metrics() string
	Quota() string
	SetPage(context.Context, string) error
	SetScreen(context.Context, bool, int) error
}

// NormalizeConfig applies local defaults. Credentials and installed assets are
// checked only when an operation needs them, keeping audio diagnostics usable.
func NormalizeConfig(c Config) (Config, error) {
	c.LLMBaseURL = strings.TrimRight(strings.TrimSpace(c.LLMBaseURL), "/")
	c.MiMoBaseURL = strings.TrimRight(strings.TrimSpace(c.MiMoBaseURL), "/")
	c.LLMAPIKey = strings.TrimSpace(c.LLMAPIKey)
	c.MiMoAPIKey = strings.TrimSpace(c.MiMoAPIKey)
	c.LLMModel = strings.TrimSpace(c.LLMModel)
	if c.LLMBaseURL == "" {
		c.LLMBaseURL = "https://api.openai.com/v1"
	}
	if c.MiMoBaseURL == "" {
		c.MiMoBaseURL = "https://api.xiaomimimo.com/v1"
	}
	if c.WakeThreshold == 0 {
		c.WakeThreshold = 0.25
	}
	// Zero volume is an intentional mute; callers may select their own initial volume.
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = "."
	}
	root := filepath.Join(cache, "Aemeath", "voice")
	if strings.TrimSpace(c.WakeModelDir) == "" {
		c.WakeModelDir = filepath.Join(root, "models")
	}
	if strings.TrimSpace(c.ReferenceAudio) == "" {
		c.ReferenceAudio = filepath.Join(root, "reference.wav")
	}
	if strings.TrimSpace(c.Persona) == "" {
		c.Persona = "你是爱弥斯，一位温柔、机敏的中文语音助手。回答简短自然，不使用 Markdown。"
	}
	if !c.Enabled {
		return c, nil
	}
	for _, entry := range []struct{ name, value string }{{"LLM", c.LLMBaseURL}, {"MiMo", c.MiMoBaseURL}} {
		u, e := url.Parse(entry.value)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("invalid %s base URL", entry.name)
		}
	}
	if math.IsNaN(c.WakeThreshold) || math.IsInf(c.WakeThreshold, 0) || c.WakeThreshold <= 0 || c.WakeThreshold > 1 {
		return c, fmt.Errorf("wake threshold must be between 0 and 1")
	}
	if c.Volume < 0 || c.Volume > 100 {
		return c, fmt.Errorf("volume must be between 0 and 100")
	}
	return c, nil
}
