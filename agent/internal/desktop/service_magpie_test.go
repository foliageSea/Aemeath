package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/magpie"
	"aemeath/agent/internal/netproxy"
	"aemeath/agent/internal/proto"
)

func TestMagpieSettingsDefaultOffAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings, err := loadSettings(path)
	if err != nil || settings.MagpieEnabled {
		t.Fatalf("new settings: enabled=%v err=%v", settings.MagpieEnabled, err)
	}
	// Legacy settings do not implicitly opt in, even with a previously valid URL.
	for _, legacy := range []string{`{}`, `{"magpieURL":"http://legacy:3425"}`} {
		if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
			t.Fatal(err)
		}
		settings, err = loadSettings(path)
		if err != nil || settings.MagpieEnabled {
			t.Fatalf("legacy %s: enabled=%v err=%v", legacy, settings.MagpieEnabled, err)
		}
	}
	for _, enabled := range []bool{false, true} {
		for _, base := range []string{"", ":bad", "http://localhost:3425/"} {
			settings, err := normalizeSettings(Settings{MagpieEnabled: enabled, MagpieURL: base})
			if enabled && base == ":bad" {
				if err == nil {
					t.Fatal("enabled backend accepted invalid URL")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			wantURL := strings.TrimSuffix(base, "/")
			if enabled && base == "" {
				wantURL = magpie.DefaultBase
			}
			if settings.MagpieURL != wantURL {
				t.Fatalf("enabled=%v URL=%q want %q", enabled, settings.MagpieURL, wantURL)
			}
			if err := persistSettings(path, settings); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadSettings(path)
			if err != nil || loaded.MagpieEnabled != enabled || loaded.MagpieURL != wantURL {
				t.Fatalf("round trip: %+v err=%v", loaded, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["magpieEnabled"]) != map[bool]string{false: "false", true: "true"}[enabled] {
				t.Fatalf("missing explicit persisted flag: %s", data)
			}
		}
	}
}

func TestMagpieDisabledDoesNotRequestOrForward(t *testing.T) {
	deps := testDeps()
	deps.quotaEvery = time.Millisecond
	var calls atomic.Int32
	deps.quota = func(context.Context, string) (*proto.QuotaFrame, error) {
		calls.Add(1)
		return nil, errors.New("must not request")
	}
	conn := &fakeConn{}
	deps.dial = func(context.Context, string) (connection, error) { return conn, nil }
	settings, err := normalizeSettings(Settings{MagpieURL: ":unfinished"})
	if err != nil {
		t.Fatal(err)
	}
	s := newService(filepath.Join(t.TempDir(), "settings.json"), settings, deps)
	t.Cleanup(s.Close)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.RefreshQuotas(); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { m, _ := conn.counts(); return m >= 5 })
	if calls.Load() != 0 {
		t.Fatalf("disabled requests = %d", calls.Load())
	}
	if _, q := conn.counts(); q != 0 {
		t.Fatalf("disabled quota sends = %d", q)
	}
	if got := (voiceHost{service: s}).Quota(); got != `{"available":false}` {
		t.Fatalf("assistant should use unavailable cache: %s", got)
	}
}

func TestMagpieDisableClearsCacheAndReenableRefreshes(t *testing.T) {
	deps := testDeps()
	var calls atomic.Int32
	var fail atomic.Bool
	source := deps.quota
	deps.quota = func(ctx context.Context, base string) (*proto.QuotaFrame, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("old gateway failure")
		}
		return source(ctx, base)
	}
	conn := &fakeConn{}
	deps.dial = func(context.Context, string) (connection, error) { return conn, nil }
	s := testService(t, deps)
	eventually(t, func() bool { return s.Snapshot().Quota != nil })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { _, q := conn.counts(); return q == 1 })
	fail.Store(true)
	if err := s.RefreshQuotas(); err == nil {
		t.Fatal("expected quota error")
	}
	if s.Snapshot().Quota == nil || s.Snapshot().QuotaError == "" {
		t.Fatal("missing cached quota/error fixture")
	}
	before := calls.Load()
	// Assistant queries only use cache, never a manual backend request.
	if got := (voiceHost{service: s}).Quota(); !strings.Contains(got, "账户A") {
		t.Fatalf("assistant cached quota: %s", got)
	}
	if calls.Load() != before {
		t.Fatal("assistant query requested quota")
	}
	settings := s.Snapshot().Settings
	settings.MagpieEnabled = false
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if state := s.Snapshot(); state.Quota != nil || state.QuotaError != "" {
		t.Fatalf("disabled cache not cleared: %+v", state)
	}
	if got := (voiceHost{service: s}).Quota(); got != `{"available":false}` {
		t.Fatalf("stale assistant quota: %s", got)
	}
	m, q := conn.counts()
	for i := 0; i < 5; i++ {
		if err := s.RefreshQuotas(); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { next, _ := conn.counts(); return next >= m+5 })
	if _, next := conn.counts(); next != q {
		t.Fatalf("disabled quota pushes: %d -> %d", q, next)
	}
	if calls.Load() != before {
		t.Fatal("disabled refresh requested quota")
	}
	fail.Store(false)
	settings.MagpieEnabled = true
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	// Poll interval is an hour: the setting change must wake an immediate refresh.
	eventually(t, func() bool { return s.Snapshot().Quota != nil && calls.Load() > before })
	eventually(t, func() bool { _, next := conn.counts(); return next > q })
}

func TestMagpieChangeCancelsAndRejectsOldResults(t *testing.T) {
	for _, change := range []string{"disable", "toggle", "url", "network"} {
		for _, staleError := range []bool{false, true} {
			name := change + map[bool]string{false: "/success", true: "/error"}[staleError]
			t.Run(name, func(t *testing.T) {
				deps := testDeps()
				entered := make(chan context.Context, 4)
				releaseOld, releaseNew := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				deps.quota = func(ctx context.Context, _ string) (*proto.QuotaFrame, error) {
					call := calls.Add(1)
					entered <- ctx
					if call == 1 {
						// Deliberately ignore cancellation, simulating a late transport result.
						<-releaseOld
						if staleError {
							return nil, errors.New("stale failure")
						}
						return &proto.QuotaFrame{V: 1, Type: "quota", Accounts: []proto.QuotaAccount{{Name: "stale"}}}, nil
					}
					select {
					case <-releaseNew:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return &proto.QuotaFrame{V: 1, Type: "quota", Accounts: []proto.QuotaAccount{{Name: "fresh"}}}, nil
				}
				// Release deliberately uncooperative dependencies before closing the service.
				s := testService(t, deps)
				t.Cleanup(func() {
					select {
					case <-releaseOld:
					default:
						close(releaseOld)
					}
					select {
					case <-releaseNew:
					default:
						close(releaseNew)
					}
				})
				next := func() context.Context {
					t.Helper()
					select {
					case ctx := <-entered:
						return ctx
					case <-time.After(time.Second):
						t.Fatal("missing quota request")
						return nil
					}
				}
				oldCtx := next()
				original := s.Snapshot().Settings
				changed := original
				switch change {
				case "disable", "toggle":
					changed.MagpieEnabled = false
				case "url":
					changed.MagpieURL = "http://other:3425"
				case "network":
					changed.Network.Mode = netproxy.ModeDirect
				}
				if err := s.SaveSettings(changed); err != nil {
					t.Fatal(err)
				}
				select {
				case <-oldCtx.Done():
				case <-time.After(time.Second):
					t.Fatal("settings change did not cancel quota request")
				}
				if change == "disable" || change == "toggle" {
					// Disabled manual refresh must not wait behind the in-flight request.
					done := make(chan error, 1)
					go func() { done <- s.RefreshQuotas() }()
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(time.Second):
						t.Fatal("disabled refresh blocked")
					}
				}
				if change == "disable" {
					close(releaseOld)
					// Joining the serialized request ensures its late result was processed.
					s.quotaMu.Lock()
					s.quotaMu.Unlock()
					if state := s.Snapshot(); state.Quota != nil || state.QuotaError != "" {
						t.Fatalf("disabled backend accepted late result: quota=%+v error=%q", state.Quota, state.QuotaError)
					}
					if calls.Load() != 1 {
						t.Fatalf("disabled backend made %d requests", calls.Load())
					}
					return
				}
				// Restore the original configuration before allowing the old result back:
				// URL/enabled/network equality alone must not admit a previous generation.
				if err := s.SaveSettings(original); err != nil {
					t.Fatal(err)
				}
				close(releaseOld)
				next()
				if state := s.Snapshot(); state.Quota != nil || state.QuotaError != "" {
					t.Fatalf("stale result accepted: quota=%+v error=%q", state.Quota, state.QuotaError)
				}
				close(releaseNew)
				eventually(t, func() bool {
					q := s.Snapshot().Quota
					return q != nil && len(q.Accounts) == 1 && q.Accounts[0].Name == "fresh"
				})
			})
		}
	}
}

func TestMagpieRestartDisabledThenEnableRefreshes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := newService(path, Settings{MagpieEnabled: false, MagpieURL: ":unfinished"}, testDeps())
	settings := s.Snapshot().Settings
	if err := s.SaveSettings(settings); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	loaded, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	deps := testDeps()
	var calls atomic.Int32
	source := deps.quota
	deps.quota = func(ctx context.Context, base string) (*proto.QuotaFrame, error) {
		calls.Add(1)
		return source(ctx, base)
	}
	restarted := newService(path, loaded, deps)
	defer restarted.Close()
	if err := restarted.RefreshQuotas(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("restart opted into disabled backend")
	}
	loaded.MagpieEnabled = true
	if err := restarted.SaveSettings(loaded); err == nil {
		t.Fatal("enabling accepted preserved invalid URL")
	}
	loaded.MagpieURL = ""
	if err := restarted.SaveSettings(loaded); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return restarted.Snapshot().Quota != nil })
	if restarted.Snapshot().Settings.MagpieURL != magpie.DefaultBase {
		t.Fatal("enable did not select default URL")
	}
	if calls.Load() == 0 {
		t.Fatal("enable after restart did not refresh")
	}
}
