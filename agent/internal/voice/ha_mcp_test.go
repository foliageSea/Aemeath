package voice

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/hamcp"
)

func TestHAMCPConfigEndpointNormalization(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"default", "", "http://homeassistant.local:8123/api/mcp/assist"},
		{"root", "http://ha.example.test:8123", "http://ha.example.test:8123/api/mcp/assist"},
		{"root slash", "https://ha.example.test/", "https://ha.example.test/api/mcp/assist"},
		{"proxy root", "https://ha.example.test/proxy/", "https://ha.example.test/proxy/api/mcp/assist"},
		{"explicit endpoint", "https://ha.example.test/api/mcp/assist", "https://ha.example.test/api/mcp/assist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := NormalizeConfig(Config{HAMCPURL: tc.raw})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HAMCPURL != tc.want {
				t.Fatalf("endpoint = %q, want %q", cfg.HAMCPURL, tc.want)
			}
		})
	}
}

func TestHAMCPConfigValidationAndRedaction(t *testing.T) {
	for _, cfg := range []Config{{}, {Enabled: true}, {HAMCPEnabled: true}, {Enabled: true, HAMCPEnabled: true}} {
		if _, err := NormalizeConfig(cfg); err != nil {
			t.Fatalf("absent token should defer validation to HA operation: %v", err)
		}
	}
	cfg, err := NormalizeConfig(Config{HAAccessToken: "  fixture-ha-token 	"})
	if err != nil || cfg.HAAccessToken != "fixture-ha-token" {
		t.Fatal("HA token was not trimmed")
	}
	if _, err := NormalizeConfig(Config{HAAccessToken: strings.Repeat("x", 4096)}); err != nil {
		t.Fatalf("4096-byte token rejected: %v", err)
	}
	for _, token := range []string{strings.Repeat("x", 4097), "fixture-ha-token" + string(rune(10)) + "injected", "fixture-ha-token" + string(rune(13)) + "injected"} {
		_, err := NormalizeConfig(Config{HAAccessToken: token})
		if err == nil {
			t.Fatal("invalid HA token accepted")
		}
		if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "fixture-ha-token") {
			t.Fatal("configuration error leaked token")
		}
	}
	for _, endpoint := range []string{"ftp://ha.example.test", "https://user:fixture-url-secret@ha.example.test", "https://ha.example.test/?token=fixture-url-secret", "http://ha.example.test/" + string(rune(10)) + "fixture-url-secret"} {
		_, err := NormalizeConfig(Config{HAMCPURL: endpoint})
		if err == nil {
			t.Fatal("invalid HA endpoint accepted")
		}
		if strings.Contains(err.Error(), endpoint) || strings.Contains(err.Error(), "fixture-url-secret") {
			t.Fatal("configuration error leaked endpoint secret")
		}
	}
	s := &session{cfg: cfg}
	got := s.safe("Bearer " + cfg.HAAccessToken + " rejected; " + cfg.HAAccessToken)
	if strings.Contains(got, cfg.HAAccessToken) || strings.Count(got, "[redacted]") != 2 {
		t.Fatalf("HA token not redacted: %q", got)
	}
}

func TestHAMCPSessionStatusUpdatesRejectStaleResults(t *testing.T) {
	var snapshots []State
	s := &session{active: true, generation: 4, state: State{Turn: 8, Phase: "thinking"}, emit: func(state State) { snapshots = append(snapshots, state) }}
	connected := hamcp.Status{Connected: true, ToolCount: 3}
	if err := s.result(result{turn: 8, generation: 4, homeAssistant: &connected}); err != nil {
		t.Fatal(err)
	}
	if s.state.HomeAssistant != connected || len(snapshots) != 1 || snapshots[0].HomeAssistant != connected {
		t.Fatal("current HA status not published")
	}
	connected.ToolCount = 99
	if s.state.HomeAssistant.ToolCount != 3 || snapshots[0].HomeAssistant.ToolCount != 3 {
		t.Fatal("status snapshot aliases producer")
	}
	failure := hamcp.Status{Error: "Home Assistant MCP 暂时不可用"}
	for _, r := range []result{
		{turn: 7, generation: 4, homeAssistant: &failure},
		{turn: 8, generation: 3, homeAssistant: &failure},
		{turn: 9, generation: 4, homeAssistant: &failure},
	} {
		if err := s.result(r); err != nil {
			t.Fatal(err)
		}
	}
	if len(snapshots) != 1 || s.state.HomeAssistant.ToolCount != 3 {
		t.Fatal("stale status applied or emitted")
	}
	s.active = false
	if err := s.result(result{turn: 8, generation: 4, homeAssistant: &failure}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || s.state.HomeAssistant.ToolCount != 3 {
		t.Fatal("inactive status applied or emitted")
	}
	s.active = true
	if err := s.result(result{turn: 8, generation: 4, homeAssistant: &failure}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || s.state.HomeAssistant != failure || s.state.Phase != "thinking" {
		t.Fatal("failure status did not update independently of voice phase")
	}
	if snapshots[0].HomeAssistant.ToolCount != 3 {
		t.Fatal("later update mutated earlier snapshot")
	}
}

type haClosingBrain struct {
	fakeBrain
	closes   atomic.Int32
	closeErr error
}

func (b *haClosingBrain) Close() error { b.closes.Add(1); return b.closeErr }

func TestHAMCPWorkerClosesBrainOnExit(t *testing.T) {
	for _, closeErr := range []error{nil, errors.New("fixture close failure")} {
		t.Run(map[bool]string{false: "success", true: "close error"}[closeErr != nil], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			engine := &haClosingBrain{closeErr: closeErr}
			dep := fakeDependencies()
			dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { return engine, nil }
			jobs := make(chan work, 1)
			out := make(chan result, 32)
			done := make(chan struct{})
			go func() {
				defer close(done)
				worker(ctx, Config{MiMoAPIKey: "fixture", LLMModel: "fixture"}, nil, dep, jobs, out)
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("worker did not exit")
				}
			}()
			for turn := uint32(1); turn <= 2; turn++ {
				jobs <- work{ctx: ctx, turn: turn, kind: "utterance", pcm: []int16{1}}
			consume:
				for {
					select {
					case r := <-out:
						if r.err != nil {
							t.Fatal(r.err)
						}
						if r.phase == "generation_done" {
							break consume
						}
					case <-ctx.Done():
						t.Fatal("worker stalled")
					}
				}
			}
			if engine.closes.Load() != 0 {
				t.Fatal("brain closed before worker exit")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("worker did not exit")
			}
			if got := engine.closes.Load(); got != 1 {
				t.Fatalf("Close called %d times, want 1", got)
			}
		})
	}
}
