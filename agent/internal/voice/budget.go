package voice

import (
	"aemeath/agent/internal/voiceapi"
	"time"
)

// Clone synthesis returns a complete WAV, and may legitimately outlast the
// former 60s HTTP/115s shared worker limits. Each stage gets its own budget;
// the outer worker is only a backstop and never cuts a fresh TTS budget short.
const (
	recognitionBudget = voiceapi.ASRRequestTimeout
	agentBudget       = 90 * time.Second
	synthesisBudget   = voiceapi.TTSRequestTimeout
	cloudWorkBudget   = recognitionBudget + agentBudget + synthesisBudget + 5*time.Second
	stageGrace        = 5 * time.Second
)

func phaseBudget(phase string) time.Duration {
	switch phase {
	case "recognizing":
		return recognitionBudget
	case "thinking":
		return agentBudget
	case "synthesizing":
		return synthesisBudget
	}
	return 0
}
