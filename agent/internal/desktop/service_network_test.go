package desktop

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/netproxy"
	"aemeath/agent/internal/proto"
	"aemeath/agent/internal/voice"
)

func networkProxyFixture(t *testing.T, marker string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			t.Error("proxy received origin-form request")
		}
		fmt.Fprint(w, marker)
	}))
	t.Cleanup(s.Close)
	return s
}

func networkRead(t *testing.T, ctx context.Context, client *http.Client) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://cloud.example.test/resource", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNetworkSettingsMigrationPersistenceAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"address":"legacy.local"}`), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := loadSettings(path)
	if err != nil || legacy.Network.Mode != netproxy.ModeSystem {
		t.Fatalf("migration mode=%q err=%v", legacy.Network.Mode, err)
	}
	for _, mode := range []string{netproxy.ModeSystem, netproxy.ModeProxy, netproxy.ModeDirect} {
		legacy.Network = netproxy.Config{Mode: mode, ProxyURL: "http://alice:secret@127.0.0.1:7890"}
		normalized, err := normalizeSettings(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := persistSettings(path, normalized); err != nil {
			t.Fatal(err)
		}
		restored, err := loadSettings(path)
		if err != nil || restored.Network != normalized.Network {
			t.Fatalf("network round trip mode=%s err=%v", mode, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("settings permissions changed")
	}
	s := testService(t, testDeps())
	settings := s.Snapshot().Settings
	settings.Network = netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "ftp://alice:secret@proxy.invalid:7890"}
	if err := s.SaveSettings(settings); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "alice") {
		t.Fatal("invalid proxy should return a sanitized error")
	}
	if s.Snapshot().Settings.Network.Mode != netproxy.ModeSystem {
		t.Fatal("invalid save published network state")
	}
}

func TestServiceNetworkContextUpdatesAndInstanceIsolation(t *testing.T) {
	a, b := networkProxyFixture(t, "proxy-a"), networkProxyFixture(t, "proxy-b")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "direct") }))
	defer origin.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "cloud.example.test:80" {
			addr = strings.TrimPrefix(origin.URL, "http://")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	client := &http.Client{Transport: netproxy.Transport(base), Timeout: time.Second}
	s1, s2 := testService(t, testDeps()), testService(t, testDeps())
	save := func(s *Service, mode, proxy string) {
		t.Helper()
		cfg := s.Snapshot().Settings
		cfg.Network = netproxy.Config{Mode: mode, ProxyURL: proxy}
		if err := s.SaveSettings(cfg); err != nil {
			t.Fatal(err)
		}
	}
	save(s1, netproxy.ModeProxy, a.URL)
	save(s2, netproxy.ModeProxy, b.URL)
	// Capture contexts once: they must continue to see the saved Router's new generation.
	ctx1, ctx2 := s1.NetworkContext(context.Background()), s2.NetworkContext(context.Background())
	if networkRead(t, ctx1, client) != "proxy-a" || networkRead(t, ctx2, client) != "proxy-b" {
		t.Fatal("service routers share policy or connections")
	}
	save(s1, netproxy.ModeProxy, b.URL)
	if networkRead(t, ctx1, client) != "proxy-b" {
		t.Fatal("saved URL did not take effect")
	}
	save(s1, netproxy.ModeDirect, a.URL)
	if networkRead(t, ctx1, client) != "direct" || networkRead(t, ctx2, client) != "proxy-b" {
		t.Fatal("direct mode or instance isolation failed")
	}
	// A failed disk save must leave the previous Router generation untouched.
	s1.lifeMu.Lock()
	previousPath := s1.path
	s1.path = t.TempDir() // cannot atomically replace an existing directory with settings
	s1.lifeMu.Unlock()
	cfg := s1.Snapshot().Settings
	cfg.Network = netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: a.URL}
	if err := s1.SaveSettings(cfg); err == nil {
		t.Fatal("expected persistence failure")
	}
	s1.lifeMu.Lock()
	s1.path = previousPath
	s1.lifeMu.Unlock()
	if networkRead(t, ctx1, client) != "direct" || s1.Snapshot().Settings.Network.Mode != netproxy.ModeDirect {
		t.Fatal("failed persistence changed active routing")
	}
	s1.Close()
	req, _ := http.NewRequestWithContext(ctx1, http.MethodGet, "http://cloud.example.test/", nil)
	if res, err := client.Do(req); err == nil {
		res.Body.Close()
		t.Fatal("closed service accepted routed request")
	}
}

func TestNetworkSaveRestartsVoiceWithoutChangingMemory(t *testing.T) {
	deps := testDeps()
	calls := make(chan standaloneCall, 4)
	var active atomic.Int32
	deps.voiceRunStandalone = func(ctx context.Context, cfg voice.Config, _ voice.Host, commands <-chan voice.Command, emit func(voice.State)) error {
		active.Add(1)
		defer active.Add(-1)
		emit(voice.State{Connected: true, Phase: "idle"})
		calls <- standaloneCall{cfg, emit, commands}
		<-ctx.Done()
		return ctx.Err()
	}
	s := testService(t, deps)
	settings := s.Snapshot().Settings
	settings.RunMode, settings.Voice.Enabled = RunModeStandalone, true
	settings.Network.Mode = netproxy.ModeDirect
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	memory := s.memory
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	first := receiveStandaloneCall(t, calls)
	settings.Network = netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "http://127.0.0.1:7890"}
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	receiveStandaloneCall(t, calls)
	if active.Load() != 1 || s.memory != memory || s.Snapshot().Settings.RunMode != RunModeStandalone {
		t.Fatal("network switch did not join old voice session or retained state was lost")
	}
	first.emit(voice.State{Response: "stale", Connected: true})
	if s.Snapshot().Voice.Response == "stale" {
		t.Fatal("old network session callback accepted")
	}
	// Saving an unchanged routing policy should not restart another voice session.
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
		t.Fatal("unchanged save restarted voice")
	default:
	}
}

func TestNetworkSaveDiscardsOldQuotaResult(t *testing.T) {
	deps := testDeps()
	entered, release := make(chan int32, 2), make(chan struct{})
	var calls atomic.Int32
	deps.quota = func(ctx context.Context, _ string) (*proto.QuotaFrame, error) {
		call := calls.Add(1)
		entered <- call
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &proto.QuotaFrame{V: 1, Accounts: []proto.QuotaAccount{{Name: fmt.Sprint(call)}}}, nil
	}
	s := testService(t, deps)
	next := func() int32 {
		t.Helper()
		select {
		case n := <-entered:
			return n
		case <-time.After(time.Second):
			t.Fatal("quota worker did not run")
			return 0
		}
	}
	if next() != 1 {
		t.Fatal("first quota request missing")
	}
	settings := s.Snapshot().Settings
	settings.Network.Mode = netproxy.ModeDirect
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	// Allow only the old request to finish, then observe the new blocked request.
	release <- struct{}{}
	if next() != 2 {
		t.Fatal("routing save did not wake quota refresh")
	}
	if s.Snapshot().Quota != nil {
		t.Fatal("old routing generation overwrote cleared quota")
	}
	release <- struct{}{}
	eventually(t, func() bool {
		q := s.Snapshot().Quota
		return q != nil && len(q.Accounts) == 1 && q.Accounts[0].Name == "2"
	})
}

func TestBackgroundHTTPAlwaysUsesDirectTransport(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	s := backgroundService(t, server, nil)
	session, err := s.beginBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.release()
	if session.client.Transport != netproxy.DirectTransport() {
		t.Fatal("device background requests inherit cloud policy")
	}
}
