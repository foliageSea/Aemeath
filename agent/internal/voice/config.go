// Package voice owns device and standalone voice sessions. The caller owns retries.
package voice

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/wake"
)

type Config struct {
	Enabled           bool    `json:"enabled"`
	LLMBaseURL        string  `json:"llmBaseURL"`
	LLMAPIKey         string  `json:"llmAPIKey"`
	LLMModel          string  `json:"llmModel"`
	ReasoningEffort   string  `json:"reasoningEffort,omitempty"`
	LLMTools          bool    `json:"llmTools"`
	ContextWindowMode string  `json:"contextWindowMode"`
	ContextWindow     int     `json:"contextWindow"`
	ModelsDevEnabled  bool    `json:"modelsDevEnabled"`
	ModelsDevProvider string  `json:"modelsDevProvider"`
	ModelsDevModel    string  `json:"modelsDevModel"`
	TavilyAPIKey      string  `json:"tavilyAPIKey"`
	HAMCPEnabled      bool    `json:"haMCPEnabled"`
	HAMCPURL          string  `json:"haMCPURL"`
	HAAccessToken     string  `json:"haAccessToken"`
	MiMoBaseURL       string  `json:"mimoBaseURL"`
	MiMoAPIKey        string  `json:"mimoAPIKey"`
	TTSProvider       string  `json:"ttsProvider"`
	FishBaseURL       string  `json:"fishBaseURL"`
	FishAPIKey        string  `json:"fishAPIKey"`
	FishModel         string  `json:"fishModel"`
	FishReferenceID   string  `json:"fishReferenceID"`
	FishReferenceText string  `json:"fishReferenceText"`
	FishLatency       string  `json:"fishLatency"`
	ReferenceAudio    string  `json:"referenceAudio"`
	WakePhrase        string  `json:"wakePhrase"`
	WakeModelDir      string  `json:"wakeModelDir"`
	WakeThreshold     float64 `json:"wakeThreshold"`
	Volume            int     `json:"volume"`
	PlaybackOutput    string  `json:"playbackOutput"`
	Persona           string  `json:"persona"`
}

type State struct {
	// ReplayKeys and ReplayTurns identify complete cached replies in this
	// connection (oldest first). ReplayingKey also covers TTS regeneration.
	ReplayKeys        []string           `json:"replayKeys,omitempty"`
	ReplayingKey      string             `json:"replayingKey,omitempty"`
	ReplayTurns       []uint32           `json:"replayTurns,omitempty"`
	ReplayingTurn     uint32             `json:"replayingTurn,omitempty"`
	Context           brain.ContextStats `json:"context"`
	ToolCalls         []brain.ToolCall   `json:"toolCalls,omitempty"`
	HomeAssistant     hamcp.Status       `json:"homeAssistant"`
	Connected         bool               `json:"connected"`
	WakeReady         bool               `json:"wakeReady"`
	Phase             string             `json:"phase"`
	Error             string             `json:"error"`
	ErrorStage        string             `json:"errorStage,omitempty"`
	Notice            string             `json:"notice,omitempty"`
	Transcript        string             `json:"transcript"`
	Response          string             `json:"response"`
	Turn              uint32             `json:"turn"`
	ASRMillis         int64              `json:"asrMillis"`
	AgentMillis       int64              `json:"agentMillis"`
	TTSMillis         int64              `json:"ttsMillis"`
	FirstAudioMillis  int64              `json:"firstAudioMillis"`
	GeneratedSegments int                `json:"generatedSegments"`
	PlayedSegments    int                `json:"playedSegments"`
	// Session identifies the voice connection. Turn numbers restart on every
	// connection, so (Session, Turn) is what identifies a turn. Assigned by the
	// host; always 0 inside this package.
	Session uint64 `json:"session"`
}

// Clone returns a snapshot with independently owned slices.
func (s State) Clone() State {
	s.ToolCalls = append(s.ToolCalls[:0:0], s.ToolCalls...)
	s.ReplayTurns = append(s.ReplayTurns[:0:0], s.ReplayTurns...)
	s.ReplayKeys = append(s.ReplayKeys[:0:0], s.ReplayKeys...)
	return s
}

type Command struct {
	Kind string
	Text string
	Turn uint32 // source turn for replay; scoped to this voice connection
}
type Host interface {
	Metrics() string
	Quota() string
	SetPage(context.Context, string) error
	SetScreen(context.Context, bool, int) error
	LockComputer(context.Context) error
}

// MemoryHost is optionally implemented by a Host whose conversation should
// survive individual voice connections (device reconnects, app restarts).
type MemoryHost interface {
	ConversationMemory() *brain.Memory
}

// ModelCatalogHost shares public catalog data across voice connections.
type ModelCatalogHost interface {
	ModelsCatalog() *brain.ModelCatalog
}

// NormalizeConfig applies local defaults. Credentials and installed assets are
// checked only when an operation needs them, keeping audio diagnostics usable.
func NormalizeConfig(c Config) (Config, error) {
	var reasoningErr error
	c.ReasoningEffort, reasoningErr = brain.NormalizeReasoningEffort(c.ReasoningEffort)
	if reasoningErr != nil {
		return c, reasoningErr
	}
	c.ContextWindowMode = strings.ToLower(strings.TrimSpace(c.ContextWindowMode))
	if c.ContextWindowMode == "" {
		c.ContextWindowMode = "auto"
	}
	if c.ContextWindowMode != "auto" && c.ContextWindowMode != "manual" {
		return c, fmt.Errorf("context window mode must be auto or manual")
	}
	c.ModelsDevProvider, c.ModelsDevModel = strings.TrimSpace(c.ModelsDevProvider), strings.TrimSpace(c.ModelsDevModel)
	for _, value := range []string{c.ModelsDevProvider, c.ModelsDevModel} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\t ") {
			return c, fmt.Errorf("invalid models.dev provider or model ID")
		}
	}
	if c.ModelsDevModel != "" && c.ModelsDevProvider == "" {
		return c, fmt.Errorf("models.dev 模型映射须同时填写服务商 ID")
	}
	var contextErr error
	c.ContextWindow, contextErr = brain.NormalizeContextWindow(c.ContextWindow)
	if contextErr != nil {
		return c, contextErr
	}
	var phraseErr error
	c.WakePhrase, phraseErr = wake.NormalizePhrase(c.WakePhrase)
	if phraseErr != nil {
		return c, phraseErr
	}
	c.LLMBaseURL = strings.TrimRight(strings.TrimSpace(c.LLMBaseURL), "/")
	c.MiMoBaseURL = strings.TrimRight(strings.TrimSpace(c.MiMoBaseURL), "/")
	c.LLMAPIKey = strings.TrimSpace(c.LLMAPIKey)
	c.TavilyAPIKey = strings.TrimSpace(c.TavilyAPIKey)
	c.HAAccessToken = strings.TrimSpace(c.HAAccessToken)
	var haErr error
	c.HAMCPURL, haErr = hamcp.NormalizeEndpoint(strings.TrimSpace(c.HAMCPURL))
	if haErr != nil {
		return c, fmt.Errorf("invalid Home Assistant MCP URL")
	}
	c.MiMoAPIKey = strings.TrimSpace(c.MiMoAPIKey)
	c.LLMModel = strings.TrimSpace(c.LLMModel)
	c.PlaybackOutput = strings.ToLower(strings.TrimSpace(c.PlaybackOutput))
	if c.PlaybackOutput == "" {
		c.PlaybackOutput = "agent"
	}
	if c.PlaybackOutput != "agent" && c.PlaybackOutput != "console" {
		return c, fmt.Errorf("playback output must be agent or console")
	}
	c.TTSProvider = strings.ToLower(strings.TrimSpace(c.TTSProvider))
	c.FishBaseURL = strings.TrimRight(strings.TrimSpace(c.FishBaseURL), "/")
	c.FishAPIKey = strings.TrimSpace(c.FishAPIKey)
	c.FishModel = strings.TrimSpace(c.FishModel)
	c.FishReferenceID = strings.TrimSpace(c.FishReferenceID)
	c.FishReferenceText = strings.TrimSpace(c.FishReferenceText)
	c.FishLatency = strings.ToLower(strings.TrimSpace(c.FishLatency))
	if c.TTSProvider == "" {
		c.TTSProvider = "mimo"
	}
	if c.FishBaseURL == "" {
		c.FishBaseURL = "https://api.fish.audio"
	}
	if c.FishModel == "" {
		c.FishModel = "s2.1-pro"
	}
	if c.FishLatency == "" {
		c.FishLatency = "balanced"
	}
	if c.TTSProvider != "mimo" && c.TTSProvider != "fish" {
		return c, fmt.Errorf("tts provider must be mimo or fish")
	}
	if c.FishLatency != "normal" && c.FishLatency != "balanced" && c.FishLatency != "low" {
		return c, fmt.Errorf("Fish latency must be low, normal or balanced")
	}
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"Fish model", c.FishModel, 128}, {"Fish reference_id", c.FishReferenceID, 256},
		{"Fish reference text", c.FishReferenceText, 16384}, {"Fish API key", c.FishAPIKey, 4096},
		{"Tavily API key", c.TavilyAPIKey, 4096},
		{"Home Assistant access token", c.HAAccessToken, 4096},
	} {
		if len(field.value) > field.limit || strings.ContainsAny(field.value, "\r\n") && field.name != "Fish reference text" {
			return c, fmt.Errorf("invalid %s", field.name)
		}
	}
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
		c.Persona = brain.DefaultPersona
	}
	if !c.Enabled {
		return c, nil
	}
	for _, entry := range []struct{ name, value string }{{"LLM", c.LLMBaseURL}, {"MiMo", c.MiMoBaseURL}, {"Fish", c.FishBaseURL}} {
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
