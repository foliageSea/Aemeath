package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aemeath/agent/internal/voice"
)

// startVoice joins the audio session to this exact telemetry connection attempt.
// Its worker is joined before connectionLoop releases or replaces that attempt.
func (s *Service) startVoice(ctx context.Context, conn connection, address string) <-chan struct{} {
	done := make(chan struct{})
	s.mu.Lock()
	cfg := s.state.Settings.Voice
	if !cfg.Enabled {
		s.state.Voice = voice.State{Phase: "disabled"}
		s.voiceCommands = nil
		s.mu.Unlock()
		close(done)
		return done
	}
	commands := make(chan voice.Command, 8)
	s.voiceCommands = commands
	s.state.Voice = voice.State{Phase: "connecting"}
	s.mu.Unlock()
	run := s.deps.voiceRun
	if run == nil {
		run = voice.Run
	}
	emit := func(state voice.State) {
		s.mu.Lock()
		if s.conn == conn && ctx.Err() == nil {
			s.state.Voice = state
		}
		s.mu.Unlock()
	}
	go func() {
		defer close(done)
		defer func() {
			s.mu.Lock()
			if s.voiceCommands == commands {
				s.voiceCommands = nil
				s.state.Voice.Connected, s.state.Voice.WakeReady = false, false
			}
			s.mu.Unlock()
		}()
		for ctx.Err() == nil {
			err := run(ctx, address, cfg, voiceHost{s}, commands, emit)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				err = errors.New("voice connection ended")
			}
			s.mu.RLock()
			state := s.state.Voice
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

// VoiceCommand queues user actions without blocking a UI or background sampler.
func (s *Service) VoiceCommand(kind, text string) error {
	switch kind {
	case "wake", "cancel", "clear", "preview", "reference":
	default:
		return fmt.Errorf("unknown voice command %q", kind)
	}
	if len(text) > 4096 {
		return errors.New("voice preview text is too long")
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

type voiceHost struct{ service *Service }

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
