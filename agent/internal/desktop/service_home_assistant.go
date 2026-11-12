package desktop

import (
	"context"
	"time"

	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/voice"
)

type haConnection interface {
	Discover(context.Context) ([]hamcp.Tool, error)
	Close() error
}

func homeAssistantConfig(cfg voice.Config) hamcp.Config {
	return hamcp.Config{Enabled: cfg.HAMCPEnabled, Endpoint: cfg.HAMCPURL, Token: cfg.HAAccessToken}
}

// HA availability belongs to the application, not the device/audio session.
// Keep a session open and refresh discovery so startup failures recover without
// a conversation or a settings-page probe. Only discovery is retried, never actions.
func (s *Service) homeAssistantLoop() {
	defer s.wg.Done()
	every := s.deps.haEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	factory := s.deps.haClient
	if factory == nil {
		factory = func(cfg hamcp.Config) (haConnection, error) { return hamcp.New(cfg) }
	}
	var client haConnection
	var current hamcp.Config
	closeClient := func() {
		if client != nil {
			_ = client.Close()
			client = nil
		}
	}
	defer closeClient()
	for {
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		cfg := homeAssistantConfig(s.state.Settings.Voice)
		ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		s.haCancel = cancel
		s.mu.Unlock()
		if cfg != current {
			closeClient()
			current = cfg
		}
		if cfg.Enabled {
			status := hamcp.Status{}
			var err error
			if client == nil {
				client, err = factory(cfg)
			}
			if err == nil {
				var tools []hamcp.Tool
				tools, err = client.Discover(ctx)
				if err == nil {
					err = ctx.Err()
				}
				if err == nil {
					status.Connected = true
					status.ToolCount = len(tools)
				}
			}
			if err != nil {
				status.Error = hamcp.ErrorMessage(err)
				closeClient()
			}
			s.mu.Lock()
			// A save cancels this attempt; an old result must never replace new settings.
			if ctx.Err() != context.Canceled && s.ctx.Err() == nil && cfg == homeAssistantConfig(s.state.Settings.Voice) {
				state := s.state.Voice.Clone()
				state.HomeAssistant = status
				s.setVoiceLocked(state)
			}
			s.mu.Unlock()
		} else {
			closeClient()
		}
		cancel()
		timer := time.NewTimer(every)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.haWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
