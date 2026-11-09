package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"aemeath/agent/internal/link"
	"aemeath/agent/internal/voice"
)

func loadVoiceConfig(path string, enabled bool) (voice.Config, error) {
	cfg := voice.Config{Volume: 70}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return cfg, fmt.Errorf("read voice configuration: %w", err)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
		if err != nil {
			return cfg, err
		}
		if len(data) > 1<<20 {
			return cfg, errors.New("voice configuration exceeds 1 MiB")
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("decode voice configuration: %w", err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return cfg, errors.New("voice configuration must contain one JSON object")
		}
	}
	cfg.Enabled = enabled || path != ""
	if value := os.Getenv("AEMEATH_LLM_BASE_URL"); value != "" {
		cfg.LLMBaseURL = value
	}
	if value := os.Getenv("AEMEATH_LLM_API_KEY"); value != "" {
		cfg.LLMAPIKey = value
	}
	if value := os.Getenv("AEMEATH_LLM_MODEL"); value != "" {
		cfg.LLMModel = value
	}
	if value := os.Getenv("MIMO_API_KEY"); value != "" {
		cfg.MiMoAPIKey = value
	}
	if value := os.Getenv("MIMO_BASE_URL"); value != "" {
		cfg.MiMoBaseURL = value
	}
	return voice.NormalizeConfig(cfg)
}

type cliVoiceControl struct {
	mu        sync.Mutex
	commands  chan voice.Command
	connected bool
}

func (c *cliVoiceControl) start(ctx context.Context, address string, cfg voice.Config, host *cliVoiceHost) <-chan struct{} {
	done := make(chan struct{})
	if !cfg.Enabled {
		close(done)
		return done
	}
	commands := make(chan voice.Command, 8)
	c.mu.Lock()
	c.commands = commands
	c.connected = false
	c.mu.Unlock()
	go func() {
		defer close(done)
		defer func() {
			c.mu.Lock()
			if c.commands == commands {
				c.commands = nil
				c.connected = false
			}
			c.mu.Unlock()
		}()
		lastPhase, lastError := "", ""
		for ctx.Err() == nil {
			err := voice.Run(ctx, address, cfg, host, commands, func(state voice.State) {
				c.mu.Lock()
				if c.commands == commands {
					c.connected = state.Connected
				}
				c.mu.Unlock()
				if state.Phase != lastPhase || state.Error != lastError {
					log.Printf("voice: %s (offline wake ready: %t)", state.Phase, state.WakeReady)
					if state.Error != "" {
						log.Printf("voice: %s", state.Error)
					}
					lastPhase, lastError = state.Phase, state.Error
				}
			})
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("voice connection: %v", err)
			}
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

func (c *cliVoiceControl) console(ctx context.Context, input io.Reader) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 8192)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		kind, text, _ := strings.Cut(line, " ")
		if line == "" {
			continue
		}
		switch kind {
		case "wake", "cancel", "clear", "preview", "reference":
		default:
			log.Print("语音命令：wake / cancel / clear / preview [文字] / reference")
			continue
		}
		c.mu.Lock()
		if !c.connected || c.commands == nil {
			log.Print("voice: device audio connection is offline")
		} else {
			select {
			case c.commands <- voice.Command{Kind: kind, Text: strings.TrimSpace(text)}:
			default:
				log.Print("voice: command queue is full")
			}
		}
		c.mu.Unlock()
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		log.Printf("voice console: %v", err)
	}
}

type cliVoiceHost struct {
	mu             sync.RWMutex
	conn           *link.Conn
	metrics, quota string
}

func (h *cliVoiceHost) setMetrics(value any) {
	b, _ := json.Marshal(value)
	h.mu.Lock()
	h.metrics = string(b)
	h.mu.Unlock()
}
func (h *cliVoiceHost) setQuota(value any) {
	b, _ := json.Marshal(value)
	h.mu.Lock()
	h.quota = string(b)
	h.mu.Unlock()
}
func (h *cliVoiceHost) Metrics() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.metrics == "" {
		return `{"available":false}`
	}
	return h.metrics
}
func (h *cliVoiceHost) Quota() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.quota == "" {
		return `{"available":false}`
	}
	return h.quota
}
func (h *cliVoiceHost) SetPage(ctx context.Context, page string) error {
	if page != "metrics" && page != "quota" && page != "voice" {
		return errors.New("invalid display page")
	}
	return h.conn.Send(ctx, struct {
		V    int    `json:"v"`
		Type string `json:"type"`
		Page string `json:"page"`
	}{1, "display", page})
}
func (h *cliVoiceHost) SetScreen(ctx context.Context, on bool, brightness int) error {
	if brightness < 0 || brightness > 100 {
		return errors.New("brightness must be 0..100")
	}
	return h.conn.Send(ctx, struct {
		V          int    `json:"v"`
		Type       string `json:"type"`
		On         bool   `json:"on"`
		Brightness int    `json:"brightness"`
	}{1, "screen", on, brightness})
}
