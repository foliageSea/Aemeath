package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/coder/websocket"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/magpie"
	"aemeath/agent/internal/proto"
)

type fakeConn struct {
	mu       sync.Mutex
	messages [][]byte
	closed   atomic.Int32
	fail     atomic.Bool
}

func (c *fakeConn) Send(ctx context.Context, v any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.fail.Load() {
		return errors.New("broken connection")
	}
	b, err := json.Marshal(v)
	c.mu.Lock()
	c.messages = append(c.messages, b)
	c.mu.Unlock()
	return err
}
func (c *fakeConn) Close() { c.closed.Add(1) }
func (c *fakeConn) hasCommand(page string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.messages {
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		if v["type"] == "display" && v["page"] == page && v["v"] == float64(1) {
			return true
		}
	}
	return false
}
func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("condition not reached")
		case <-ticker.C:
		}
	}
}
func testDeps() dependencies {
	return dependencies{
		sample: func(context.Context) proto.Frame {
			return proto.Frame{V: 1, CPU: &proto.CPU{Total: proto.F(12), Cores: []float64{12}}}
		},
		close: func() {},
		find:  func(ctx context.Context) (string, error) { return "device:80", nil },
		dial:  func(context.Context, string) (connection, error) { return &fakeConn{}, nil },
		quota: func(context.Context, string) (*proto.QuotaFrame, error) {
			return &proto.QuotaFrame{V: 1, Type: "quota", Accounts: []proto.QuotaAccount{{Name: "账户A", Windows: []proto.QuotaWindow{{Name: "daily", Used: 5}}}}}, nil
		},
		sampleEvery: 5 * time.Millisecond, quotaEvery: time.Hour, retryEvery: 5 * time.Millisecond,
	}
}
func testService(t *testing.T, deps dependencies) *Service {
	t.Helper()
	settings, err := normalizeSettings(Settings{})
	if err != nil {
		t.Fatal(err)
	}
	s := newService(filepath.Join(t.TempDir(), "settings.json"), settings, deps)
	t.Cleanup(s.Close)
	return s
}

func TestPreferencesPersistenceAndOwnership(t *testing.T) {
	s := testService(t, testDeps())
	settings := Settings{Address: " device:80 ", MagpieURL: "http://localhost:3425/", Subscriptions: []Subscription{{Name: "账户A", Hidden: true}, {Name: "B"}}}
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	settings.Subscriptions[0].Name = "mutated input"
	got, err := loadSettings(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "device:80" || got.MagpieURL != "http://localhost:3425" || got.Subscriptions[0].Name != "账户A" {
		t.Fatalf("bad persisted settings: %+v", got)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	snapshot := s.Snapshot()
	snapshot.Settings.Subscriptions[0].Name = "mutated snapshot"
	if s.Snapshot().Settings.Subscriptions[0].Name != "账户A" {
		t.Fatal("snapshot aliases service preferences")
	}
	if err := s.SaveSettings(Settings{MagpieURL: ":bad"}); err == nil {
		t.Fatal("invalid URL accepted")
	}
	if err := s.SaveSettings(Settings{Subscriptions: []Subscription{{Name: "A"}, {Name: "A"}}}); err == nil {
		t.Fatal("duplicate names accepted")
	}
	if s.Snapshot().Settings.Address != "device:80" {
		t.Fatal("invalid save changed state")
	}
}

func TestSaveFailurePreservesPreferences(t *testing.T) {
	s := testService(t, testDeps())
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(parent, "settings.json")
	before := s.Snapshot().Settings
	if err := s.SaveSettings(Settings{Address: "other:80"}); err == nil {
		t.Fatal("write to non-directory succeeded")
	}
	if !reflect.DeepEqual(s.Snapshot().Settings, before) {
		t.Fatal("failed persistence changed live settings")
	}
}

func TestOfflineSamplingRefreshAndSnapshotOwnership(t *testing.T) {
	s := testService(t, testDeps())
	eventually(t, func() bool { state := s.Snapshot(); return state.Metrics != nil && state.Quota != nil })
	if s.Snapshot().Running {
		t.Fatal("New enabled device forwarding")
	}
	if err := s.SetPage("quota"); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline error = %v", err)
	}
	if err := s.RefreshQuotas(); err != nil {
		t.Fatal(err)
	}
	state := s.Snapshot()
	*state.Metrics.CPU.Total = 99
	state.Metrics.CPU.Cores[0] = 99
	state.Quota.Accounts[0].Windows[0].Used = 99
	state = s.Snapshot()
	if *state.Metrics.CPU.Total != 12 || state.Metrics.CPU.Cores[0] != 12 || state.Quota.Accounts[0].Windows[0].Used != 5 {
		t.Fatal("snapshot aliases sampled data")
	}
	s.Close()
	s.Close()
	if s.Snapshot().Status != "closed" {
		t.Fatal("Close did not settle state")
	}
	if !errors.Is(s.Start(), ErrClosed) || !errors.Is(s.RefreshQuotas(), ErrClosed) || !errors.Is(s.SaveSettings(Settings{}), ErrClosed) {
		t.Fatal("closed service accepted operation")
	}
}

func TestLifecyclePageReconnectAndStop(t *testing.T) {
	deps := testDeps()
	var dials atomic.Int32
	var closeCount atomic.Int32
	connections := make(chan *fakeConn, 10)
	deps.close = func() { closeCount.Add(1) }
	deps.dial = func(context.Context, string) (connection, error) {
		dials.Add(1)
		c := &fakeConn{}
		connections <- c
		return c, nil
	}
	s := testService(t, deps)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	first := <-connections
	eventually(t, func() bool { return s.Snapshot().Connected })
	if err := s.SetPage("quota"); err != nil {
		t.Fatal(err)
	}
	if !first.hasCommand("quota") || s.Snapshot().Page != "quota" {
		t.Fatal("page command not sent")
	}
	if err := s.SetPage("invalid"); err == nil {
		t.Fatal("invalid page accepted")
	}
	first.fail.Store(true)
	eventually(t, func() bool { return dials.Load() >= 2 && s.Snapshot().Connected })
	second := <-connections
	eventually(t, func() bool { return second.hasCommand("quota") })
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Running || s.Snapshot().Connected || first.closed.Load() != 1 || second.closed.Load() != 1 {
		t.Fatal("Stop failed to join connection workers")
	}
	if err := s.RefreshQuotas(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Snapshot().Connected })
	s.Close()
	if closeCount.Load() != 1 {
		t.Fatal("collector close count", closeCount.Load())
	}
}

func TestStopCancelsDiscoveryAndCloseCancelsQuota(t *testing.T) {
	deps := testDeps()
	discovering := make(chan struct{})
	deps.find = func(ctx context.Context) (string, error) { close(discovering); <-ctx.Done(); return "", ctx.Err() }
	fetching := make(chan struct{})
	deps.quota = func(ctx context.Context, _ string) (*proto.QuotaFrame, error) {
		close(fetching)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s := testService(t, deps)
	<-fetching
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	<-discovering
	done := make(chan struct{})
	go func() { _ = s.Stop(); s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked shutdown")
	}
}

func TestSubscriptionOrderHideBeforeDeviceLimit(t *testing.T) {
	full := &proto.QuotaFrame{V: 1, Type: "quota", Usage: &proto.QuotaUsage{CallsToday: 42}}
	for i := 0; i < 12; i++ {
		full.Accounts = append(full.Accounts, proto.QuotaAccount{Name: fmt.Sprintf("account-%02d", i)})
	}
	preferences := []Subscription{{Name: "account-11"}, {Name: "account-00", Hidden: true}, {Name: "account-09"}, {Name: "missing"}}
	ordered := orderedQuota(full, preferences)
	device := magpie.ForDevice(ordered)
	expected := []string{"account-11", "account-09", "account-01", "account-02", "account-03", "account-04", "account-05", "account-06"}
	var names []string
	for _, account := range device.Accounts {
		names = append(names, account.Name)
	}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("device accounts = %v", names)
	}
	if len(full.Accounts) != 12 || full.Accounts[0].Name != "account-00" || device.Usage.CallsToday != 42 {
		t.Fatal("filter destroyed full UI frame")
	}
	ordered.Accounts[0].Name = "changed"
	if full.Accounts[11].Name != "account-11" {
		t.Fatal("ordered frame aliases input")
	}
}

func (c *fakeConn) counts() (metrics, quotas int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.messages {
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		if v["type"] == "quota" {
			quotas++
		}
		if v["cpu"] != nil {
			metrics++
		}
	}
	return
}

func TestQuotaOnlyResentWhenChangedAndGatewayClears(t *testing.T) {
	deps := testDeps()
	conn := &fakeConn{}
	deps.dial = func(context.Context, string) (connection, error) { return conn, nil }
	source := deps.quota
	deps.quota = func(ctx context.Context, base string) (*proto.QuotaFrame, error) {
		if base != "http://127.0.0.1:3425" {
			return nil, errors.New("gateway unavailable")
		}
		return source(ctx, base)
	}
	s := testService(t, deps)
	eventually(t, func() bool { return s.Snapshot().Quota != nil })
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { m, _ := conn.counts(); return m >= 5 })
	_, q := conn.counts()
	if q != 1 {
		t.Fatal("unchanged quota sent", q, "times")
	}
	settings := s.Snapshot().Settings
	settings.Subscriptions = []Subscription{{Name: "账户A", Hidden: true}}
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { _, q := conn.counts(); return q == 2 })
	settings.MagpieURL = "http://other:3425"
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { _, q := conn.counts(); return q == 3 })
	before, _ := conn.counts()
	eventually(t, func() bool { m, _ := conn.counts(); return m >= before+5 })
	_, q = conn.counts()
	if q != 3 {
		t.Fatal("empty gateway reset repeated", q)
	}
	if s.Snapshot().Quota != nil {
		t.Fatal("old gateway quota retained")
	}
}

func TestAddressValidation(t *testing.T) {
	for _, address := range []string{"", "device", "aemeath.local", "192.168.1.2", "device:80", "[::1]:8080", "::1", "[::1]"} {
		if _, err := normalizeSettings(Settings{Address: address}); err != nil {
			t.Errorf("valid address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"ws://device", "http://device/ws", "device/path", "device:bad", "device:0", "device:65536", ":80", "has space", "-bad.local", "a..b"} {
		if _, err := normalizeSettings(Settings{Address: address}); err == nil {
			t.Errorf("invalid address %q accepted", address)
		}
	}
}

func TestConcurrentLifecycleSettingsAndRefresh(t *testing.T) {
	s := testService(t, testDeps())
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				var err error
				switch worker {
				case 0:
					err = s.Start()
					if err == nil {
						err = s.Stop()
					}
				case 1:
					err = s.SaveSettings(Settings{Address: fmt.Sprintf("device-%d:80", i), Subscriptions: []Subscription{{Name: "账户A", Hidden: i%2 == 0}}})
				case 2:
					err = s.RefreshQuotas()
				case 3:
					_ = s.Snapshot()
					err = s.SetPage("quota")
				}
				if err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrOffline) && !errors.Is(err, context.Canceled) {
					t.Errorf("concurrent operation: %v", err)
				}
			}
		}(worker)
	}
	wg.Wait()
	disk, err := loadSettings(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(disk, s.Snapshot().Settings) {
		t.Fatal("disk and live preferences diverged")
	}
	s.Close()
}

// Exercise New's real link.Dial adapter: a failed websocket handshake returns a
// nil *link.Conn, which must not become a non-nil connection interface during
// failure cleanup. Then verify that the same service recovers on its next retry.
func TestNewSurvivesRealDialFailureAndReconnects(t *testing.T) {
	var accept atomic.Bool
	var attempts atomic.Int32
	messages := make(chan map[string]any, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/magpie/quotas":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/v1/magpie/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"calls":0,"cost":0,"series":[]}`))
		case "/ws":
			attempts.Add(1)
			if !accept.Load() {
				http.Error(w, "device unavailable", http.StatusServiceUnavailable)
				return
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			for {
				_, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				var frame map[string]any
				if json.Unmarshal(data, &frame) != nil {
					return
				}
				select {
				case messages <- frame:
				case <-r.Context().Done():
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := persistSettings(path, Settings{Address: strings.TrimPrefix(server.URL, "http://"), MagpieURL: server.URL, Subscriptions: []Subscription{}}); err != nil {
		t.Fatal(err)
	}
	service, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		state := service.Snapshot()
		return attempts.Load() > 0 && state.Running && !state.Connected && state.Error != ""
	})
	if err := service.SetPage("quota"); !errors.Is(err, ErrOffline) {
		t.Fatalf("SetPage during dial failure = %v", err)
	}
	accept.Store(true)
	// The production retry interval is two seconds; allow enough time to observe
	// a real recovered websocket and an actual host metrics frame.
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	gotMetrics := false
	for !gotMetrics {
		select {
		case frame := <-messages:
			gotMetrics = frame["cpu"] != nil && frame["v"] == float64(1)
		case <-deadline.C:
			t.Fatal("service failed to reconnect and send real metrics")
		}
	}
	state := service.Snapshot()
	if attempts.Load() < 2 || !state.Connected || state.Error != "" || state.Metrics == nil {
		t.Fatalf("failed recovery: %+v, attempts=%d", state, attempts.Load())
	}
	if err := service.SetPage("quota"); err != nil {
		t.Fatal(err)
	}
	gotPage := false
	for !gotPage {
		select {
		case frame := <-messages:
			gotPage = frame["type"] == "display" && frame["page"] == "quota" && frame["v"] == float64(1)
		case <-deadline.C:
			t.Fatal("real websocket did not receive page command")
		}
	}
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}
	if service.Snapshot().Connected || service.Snapshot().Running {
		t.Fatal("Stop did not settle recovered service")
	}
	service.Close()
	if service.Snapshot().Status != "closed" {
		t.Fatal("Close did not settle recovered service")
	}
}

func TestAddressChangeCancelsAttempt(t *testing.T) {
	deps := testDeps()
	attempts := make(chan string, 4)
	deps.dial = func(ctx context.Context, address string) (connection, error) {
		attempts <- address
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s := testService(t, deps)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	<-attempts
	settings := s.Snapshot().Settings
	settings.Address = "new-device:81"
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	select {
	case address := <-attempts:
		if address != "new-device:81" {
			t.Fatal(address)
		}
	case <-time.After(time.Second):
		t.Fatal("address change did not cancel dial")
	}
}
