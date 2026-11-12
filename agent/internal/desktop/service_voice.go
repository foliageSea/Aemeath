package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/sessionlock"
	"aemeath/agent/internal/voice"
	"aemeath/agent/internal/wake"
)

// startVoice joins the audio session to this exact telemetry connection attempt.
// Its worker is joined before connectionLoop releases or replaces that attempt.
func (s *Service) startVoice(ctx context.Context, conn connection, address string) <-chan struct{} {
	done := make(chan struct{})
	s.mu.Lock()
	cfg := s.state.Settings.Voice
	if !cfg.Enabled {
		s.setVoiceLocked(voice.State{Phase: "disabled", HomeAssistant: s.state.Voice.HomeAssistant})
		s.voiceCommands = nil
		s.mu.Unlock()
		close(done)
		return done
	}
	commands := make(chan voice.Command, 8)
	s.voiceCommands = commands
	s.setVoiceLocked(voice.State{Phase: "connecting", HomeAssistant: s.state.Voice.HomeAssistant})
	s.mu.Unlock()
	run := s.deps.voiceRun
	if run == nil {
		run = voice.Run
	}
	var session uint64
	var lastHA hamcp.Status
	emit := func(state voice.State) {
		state.Session = session
		s.mu.Lock()
		report := false
		if s.conn == conn && ctx.Err() == nil {
			previous := s.state.Voice
			report = state.Error != "" && (state.Error != previous.Error || state.Turn != previous.Turn || state.Phase != previous.Phase)
			if state.HomeAssistant == lastHA {
				state.HomeAssistant = previous.HomeAssistant
			} else {
				lastHA = state.HomeAssistant
			}
			s.setVoiceLocked(state)
		}
		s.mu.Unlock()
		if report {
			// Runtime errors are sanitized; never log keys, transcripts or response audio.
			log.Printf("voice: turn=%d phase=%s asr_ms=%d agent_ms=%d tts_ms=%d error=%s", state.Turn, state.Phase, state.ASRMillis, state.AgentMillis, state.TTSMillis, state.Error)
		}
	}
	go func() {
		defer close(done)
		defer func() {
			s.mu.Lock()
			if s.voiceCommands == commands {
				s.voiceCommands = nil
				state := s.state.Voice.Clone()
				state.Connected, state.WakeReady = false, false
				s.setVoiceLocked(state)
			}
			s.mu.Unlock()
		}()
		for ctx.Err() == nil {
			session = s.voiceSession.Add(1)
			lastHA = hamcp.Status{}
			err := run(ctx, address, cfg, voiceHost{s}, commands, emit)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				err = errors.New("voice connection ended")
			}
			s.mu.RLock()
			state := s.state.Voice.Clone()
			s.mu.RUnlock()
			state.Connected, state.WakeReady, state.Phase, state.Error = false, false, "error", err.Error()
			emit(state)
			// Discard commands queued against the ended session before reconnecting.
			for len(commands) > 0 {
				<-commands
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return done
}

// VoiceConversation is the remembered conversation the assistant will
// continue, for redisplay after an app restart.
func (s *Service) VoiceConversation() []brain.MemoryTurn { return s.memory.Turns() }

// VoiceCommand queues user actions without blocking a UI or background sampler.
func (s *Service) VoiceCommand(kind, text string) error {
	switch kind {
	case "wake", "cancel", "clear", "preview", "reference", "record_start", "record_stop", "record_cancel", "text":
	default:
		return fmt.Errorf("unknown voice command %q", kind)
	}
	if len(text) > 4096 {
		return errors.New("voice command text is too long (maximum 4096 bytes)")
	}
	if kind == "text" {
		text = strings.TrimSpace(text)
		if text == "" {
			return errors.New("voice command text must not be empty")
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	if !s.state.Settings.Voice.Enabled {
		return errors.New("voice assistant is disabled")
	}
	if !s.state.Voice.Connected || s.voiceCommands == nil {
		return ErrOffline
	}
	select {
	case s.voiceCommands <- voice.Command{Kind: kind, Text: text}:
		return nil
	default:
		return errors.New("voice assistant is busy; try again shortly")
	}
}

// ReloadWakeModels reconnects the saved voice configuration after an explicit
// model install. Draft directories never replace or save the running settings.
func (s *Service) ReloadWakeModels(modelDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	saved := strings.TrimSpace(s.state.Settings.Voice.WakeModelDir)
	if saved == "" {
		saved = wake.DefaultModelDir()
	}
	if filepath.Clean(saved) == filepath.Clean(modelDir) && s.state.Settings.Voice.Enabled && s.attemptCancel != nil {
		s.attemptCancel()
	}
	return nil
}

type voiceHost struct{ service *Service }

func (h voiceHost) ConversationMemory() *brain.Memory { return h.service.memory }

func (h voiceHost) ModelsCatalog() *brain.ModelCatalog { return h.service.catalog }

func (s *Service) refreshModelCatalog(cfg voice.Config) {
	if cfg.ModelsDevEnabled && cfg.ContextWindowMode != "manual" && s.catalog != nil {
		s.catalog.RefreshAsync(s.ctx)
	}
}

func (s *Service) modelCatalogLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.mu.RLock()
			cfg := s.state.Settings.Voice
			s.mu.RUnlock()
			s.refreshModelCatalog(cfg)
		}
	}
}

func (h voiceHost) LockComputer(ctx context.Context) error { return sessionlock.Lock(ctx) }

func (h voiceHost) Metrics() string {
	state := h.service.Snapshot()
	if state.Metrics == nil {
		return `{"available":false}`
	}
	b, _ := json.Marshal(state.Metrics)
	return string(b)
}
func (h voiceHost) Quota() string {
	state := h.service.Snapshot()
	if state.Quota == nil {
		b, _ := json.Marshal(struct {
			Available bool   `json:"available"`
			Error     string `json:"error,omitempty"`
		}{false, state.QuotaError})
		return string(b)
	}
	b, _ := json.Marshal(state.Quota)
	return string(b)
}
func (h voiceHost) SetPage(ctx context.Context, page string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.service.setPage(ctx, page)
}
func (h voiceHost) SetScreen(ctx context.Context, on bool, brightness int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.service.setScreen(ctx, on, brightness)
}
