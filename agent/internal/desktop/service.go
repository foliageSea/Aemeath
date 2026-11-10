package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"

	"aemeath/agent/internal/collect"
	"aemeath/agent/internal/discover"
	"aemeath/agent/internal/link"
	"aemeath/agent/internal/magpie"
	"aemeath/agent/internal/proto"
	"aemeath/agent/internal/voice"
)

var (
	ErrClosed  = errors.New("desktop service is closed")
	ErrOffline = errors.New("device is offline")
)

type connection interface {
	Send(context.Context, any) error
	// Incoming yields device frames and closes when the connection ends.
	// A nil channel means the device never reports.
	Incoming() <-chan []byte
	Close()
}
type dependencies struct {
	sample                              func(context.Context) proto.Frame
	close                               func()
	find                                func(context.Context) (string, error)
	dial                                func(context.Context, string) (connection, error)
	quota                               func(context.Context, string) (*proto.QuotaFrame, error)
	voiceRun                            func(context.Context, string, voice.Config, voice.Host, <-chan voice.Command, func(voice.State)) error
	sampleEvery, quotaEvery, retryEvery time.Duration
}

// Service owns local sampling and quota polling independently of device forwarding.
// Use Close to release all resources; Stop only disables device forwarding.
type Service struct {
	lifeMu        sync.Mutex
	mu            sync.RWMutex
	sendMu        sync.Mutex
	quotaMu       sync.Mutex
	state         State
	path          string
	deps          dependencies
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	wg            sync.WaitGroup
	runCancel     context.CancelFunc
	runDone       chan struct{}
	attemptCancel context.CancelFunc
	conn          connection
	connCtx       context.Context
	updates       chan struct{}
	quotaWake     chan struct{}
	voiceCommands chan voice.Command
}

// New loads preferences and starts local sampling (1s) and quota polling (30s).
// Device forwarding remains stopped until Start is called.
func New(configPath string) (*Service, error) {
	settings, err := loadSettings(configPath)
	if err != nil {
		return nil, err
	}
	c := collect.New()
	return newService(configPath, settings, dependencies{
		sample: c.Sample, close: c.Close,
		find: func(ctx context.Context) (string, error) {
			// discover.Find's DNS fallback currently ignores cancellation. Isolate that
			// call so shutdown never waits for an OS resolver timeout.
			type result struct {
				address string
				err     error
			}
			ch := make(chan result, 1)
			go func() { address, err := discover.Find(ctx, 4*time.Second); ch <- result{address, err} }()
			select {
			case r := <-ch:
				return r.address, r.err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		dial: func(ctx context.Context, address string) (connection, error) {
			conn, err := link.Dial(ctx, address)
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
		quota: func(ctx context.Context, base string) (*proto.QuotaFrame, error) {
			return magpie.New(base).FrameAll(ctx)
		},
		voiceRun:    voice.Run,
		sampleEvery: time.Second, quotaEvery: 30 * time.Second, retryEvery: 2 * time.Second,
	}), nil
}

func newService(path string, settings Settings, deps dependencies) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{path: path, deps: deps, ctx: ctx, cancel: cancel, state: State{Settings: settings, Status: "stopped", Page: "metrics", ScreenOn: true, Brightness: 80}, updates: make(chan struct{}, 1), quotaWake: make(chan struct{}, 1)}
	s.wg.Add(2)
	go s.sampleLoop()
	go s.quotaLoop()
	return s
}

func (s *Service) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Clone()
}

// Start is idempotent and starts asynchronous discovery, connection and retry.
func (s *Service) Start() error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.state.Running {
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.runCancel, s.runDone = cancel, make(chan struct{})
	s.state.Running, s.state.Status, s.state.Error = true, "connecting", ""
	s.wg.Add(1)
	go s.connectionLoop(ctx, s.runDone)
	return nil
}

// Stop is idempotent and waits for the connection loop to exit.
func (s *Service) Stop() error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	s.stop()
	return nil
}
func (s *Service) stop() {
	s.mu.Lock()
	cancel, done := s.runCancel, s.runDone
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	s.mu.Lock()
	s.runCancel, s.runDone = nil, nil
	s.state.Running, s.state.Connected, s.state.Status = false, false, "stopped"
	s.state.Target, s.state.Error = "", ""
	s.state.ScreenKnown = false
	s.state.Voice.Connected, s.state.Voice.WakeReady = false, false
	s.state.Voice.Phase = "stopped"
	s.mu.Unlock()
}

func (s *Service) SaveSettings(settings Settings) error {
	normalized, err := normalizeSettings(settings)
	if err != nil {
		return err
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if err := persistSettings(s.path, normalized); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	s.mu.Lock()
	old := s.state.Settings
	s.state.Settings = normalized
	if (old.Address != normalized.Address || old.Voice != normalized.Voice) && s.attemptCancel != nil {
		s.attemptCancel()
	}
	if old.MagpieURL != normalized.MagpieURL {
		s.state.Quota, s.state.QuotaError = nil, ""
	}
	s.mu.Unlock()
	signal(s.updates)
	if old.MagpieURL != normalized.MagpieURL {
		signal(s.quotaWake)
	}
	return nil
}

type displayFrame struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	Page string `json:"page"`
}

// SetPage sends a display command; it changes Page only after a successful send.
func (s *Service) SetPage(page string) error {
	return s.setPage(context.Background(), page)
}

func (s *Service) setPage(request context.Context, page string) error {
	if err := request.Err(); err != nil {
		return err
	}
	if page != "metrics" && page != "quota" && page != "voice" {
		return fmt.Errorf("invalid page %q: expected metrics, quota or voice", page)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	conn, connectionCtx := s.conn, s.connCtx
	if conn == nil || !s.state.Connected {
		s.mu.Unlock()
		return ErrOffline
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(request)
	stop := context.AfterFunc(connectionCtx, cancel)
	defer func() { stop(); cancel() }()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conn.Send(ctx, displayFrame{1, "display", page}); err != nil {
		if request.Err() == nil {
			s.failConnection(conn, err)
		}
		return fmt.Errorf("set page: %w", err)
	}
	s.mu.Lock()
	if s.conn == conn {
		s.state.Page, s.state.LastSent = page, time.Now().Unix()
	}
	s.mu.Unlock()
	return nil
}

type screenFrame struct {
	V          int    `json:"v"`
	Type       string `json:"type"`
	On         bool   `json:"on"`
	Brightness int    `json:"brightness"`
}

// SetScreen sends a backlight command (screen on/off and brightness 0..100);
// it changes ScreenOn/Brightness only after a successful send.
func (s *Service) SetScreen(on bool, brightness int) error {
	return s.setScreen(context.Background(), on, brightness)
}

func (s *Service) setScreen(request context.Context, on bool, brightness int) error {
	if err := request.Err(); err != nil {
		return err
	}
	if brightness < 0 || brightness > 100 {
		return fmt.Errorf("invalid brightness %d: expected 0..100", brightness)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	conn, connectionCtx := s.conn, s.connCtx
	if conn == nil || !s.state.Connected {
		s.mu.Unlock()
		return ErrOffline
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(request)
	stop := context.AfterFunc(connectionCtx, cancel)
	defer func() { stop(); cancel() }()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conn.Send(ctx, screenFrame{1, "screen", on, brightness}); err != nil {
		if request.Err() == nil {
			s.failConnection(conn, err)
		}
		return fmt.Errorf("set screen: %w", err)
	}
	s.mu.Lock()
	if s.conn == conn {
		s.state.ScreenOn, s.state.Brightness, s.state.LastSent = on, brightness, time.Now().Unix()
	}
	s.mu.Unlock()
	return nil
}

// RefreshQuotas synchronously refreshes the full UI list, including when stopped.
func (s *Service) RefreshQuotas() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return ErrClosed
	}
	s.mu.RLock()
	base := s.state.Settings.MagpieURL
	s.mu.RUnlock()
	frame, err := s.deps.quota(s.ctx, base)
	s.mu.Lock()
	if s.ctx.Err() == nil && s.state.Settings.MagpieURL == base {
		if err != nil {
			s.state.QuotaError = err.Error()
		} else {
			s.state.Quota, s.state.QuotaError = cloneQuota(frame), ""
		}
	}
	s.mu.Unlock()
	if err == nil {
		signal(s.updates)
	}
	return err
}

// Close is idempotent, cancels requests, joins workers, and closes the collector.
func (s *Service) Close() {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.stop()
	s.wg.Wait()
	s.deps.close()
	s.mu.Lock()
	s.state.Status = "closed"
	s.mu.Unlock()
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (s *Service) sampleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.deps.sampleEvery)
	defer ticker.Stop()
	for {
		frame := s.deps.sample(s.ctx)
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.state.Metrics = cloneMetrics(&frame)
		s.mu.Unlock()
		signal(s.updates)
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) quotaLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.deps.quotaEvery)
	defer ticker.Stop()
	for {
		_ = s.RefreshQuotas()
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.quotaWake:
		}
	}
}

func deviceAddress(address string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(strings.Trim(address, "[]"), "80")
}

func (s *Service) failConnection(conn connection, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == conn {
		s.state.Connected, s.state.Error, s.state.Status = false, err.Error(), "reconnecting"
		if s.attemptCancel != nil {
			s.attemptCancel()
		}
	}
}

func (s *Service) connectionLoop(ctx context.Context, done chan struct{}) {
	defer s.wg.Done()
	defer close(done)
	for ctx.Err() == nil {
		attempt, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.attemptCancel = cancel
		address := s.state.Settings.Address
		s.state.Status, s.state.Connected = "connecting", false
		if address == "" {
			s.state.Status = "discovering"
		}
		s.mu.Unlock()
		var err error
		if address == "" {
			address, err = s.deps.find(attempt)
		} else {
			address = deviceAddress(address)
		}
		var conn connection
		if err == nil && attempt.Err() == nil {
			s.mu.Lock()
			s.state.Target = address
			s.mu.Unlock()
			conn, err = s.deps.dial(attempt, address)
		}
		if err == nil && conn != nil && attempt.Err() == nil {
			s.mu.Lock()
			s.conn, s.connCtx = conn, attempt
			s.state.Connected, s.state.Status, s.state.Error = true, "connected", ""
			s.mu.Unlock()
			voiceCtx, stopVoice := context.WithCancel(attempt)
			voiceDone := s.startVoice(voiceCtx, conn, address)
			err = s.forward(attempt, conn)
			// Join audio without changing whether this attempt was externally
			// canceled; ordinary socket failures must still observe retry backoff.
			stopVoice()
			<-voiceDone
		}
		changed := attempt.Err() != nil && ctx.Err() == nil
		cancel()
		s.mu.Lock()
		s.conn, s.connCtx, s.attemptCancel = nil, nil, nil
		s.state.Connected, s.state.ScreenKnown = false, false
		if err != nil && ctx.Err() == nil {
			s.state.Error, s.state.Status = err.Error(), "reconnecting"
		}
		s.mu.Unlock()
		if conn != nil {
			conn.Close()
		}
		if ctx.Err() != nil {
			return
		}
		if changed {
			continue
		}
		timer := time.NewTimer(s.deps.retryEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) forward(ctx context.Context, conn connection) error {
	s.sendMu.Lock()
	// Screen state is owned by the device (persisted there and reported on
	// connect), so only the page is pushed here.
	err := conn.Send(ctx, displayFrame{1, "display", s.Snapshot().Page})
	s.sendMu.Unlock()
	if err != nil {
		return err
	}
	incoming := conn.Incoming()
	var lastQuota *proto.QuotaFrame
	for {
		state := s.Snapshot()
		s.sendMu.Lock()
		if state.Metrics != nil {
			err = conn.Send(ctx, state.Metrics)
		}
		quota := magpie.ForDevice(orderedQuota(state.Quota, state.Settings.Subscriptions))
		quotaChanged := !reflect.DeepEqual(quota, lastQuota)
		if err == nil && quotaChanged {
			payload := quota
			if payload == nil {
				payload = &proto.QuotaFrame{V: 1, Type: "quota", TS: time.Now().Unix(), Accounts: []proto.QuotaAccount{}}
			}
			err = conn.Send(ctx, payload)
			if err == nil {
				lastQuota = quota
			}
		}
		s.sendMu.Unlock()
		if err != nil {
			return err
		}
		if state.Metrics != nil || quotaChanged {
			s.mu.Lock()
			if s.conn == conn {
				s.state.LastSent = time.Now().Unix()
			}
			s.mu.Unlock()
		}
		if err := s.waitForUpdate(ctx, conn, incoming); err != nil {
			return err
		}
	}
}

// waitForUpdate blocks until there is something new to push, applying device
// reports as they arrive (they do not require sending anything back).
func (s *Service) waitForUpdate(ctx context.Context, conn connection, incoming <-chan []byte) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.updates:
			return nil
		case msg, ok := <-incoming:
			if !ok {
				return errors.New("device closed the connection")
			}
			s.handleDeviceFrame(conn, msg)
		}
	}
}

type deviceFrame struct {
	V          int    `json:"v"`
	Type       string `json:"type"`
	On         *bool  `json:"on"`
	Brightness *int   `json:"brightness"`
}

// handleDeviceFrame applies reports from the device, which is the source of truth
// for screen power and brightness.
func (s *Service) handleDeviceFrame(conn connection, msg []byte) {
	var f deviceFrame
	if json.Unmarshal(msg, &f) != nil || f.V != 1 || f.Type != "screen_state" || f.On == nil || f.Brightness == nil {
		return
	}
	brightness := min(max(*f.Brightness, 0), 100)
	s.mu.Lock()
	if s.conn == conn {
		s.state.ScreenOn, s.state.Brightness, s.state.ScreenKnown = *f.On, brightness, true
	}
	s.mu.Unlock()
}
