package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"aemeath/agent/internal/audioinput"
	"aemeath/agent/internal/brain"

	"github.com/coder/websocket"
)

const maxPlaybackSamples = sampleRate * 120

type incoming struct {
	kind websocket.MessageType
	data []byte
	err  error
}
type playback struct {
	pcm                   []int16
	offset                int
	seq                   uint32
	credits               int
	ended, playing        bool
	deadline              time.Time
	streaming, inputEnded bool
	received              int
}
type session struct {
	ctx                        context.Context
	ws                         *websocket.Conn
	standalone                 bool
	cfg                        Config
	state                      State
	emit                       func(State)
	epoch                      uint32
	detector                   detector
	vad                        vad
	diagnostic                 string
	ring                       sampleRing
	kwsSamples                 int64
	capture                    *capture
	manualFixed                bool
	recording                  bool
	muted                      bool
	nextMic                    uint32
	jobs                       chan work
	results                    chan result
	responses                  chan result
	lastResponseWire           time.Time
	cancelTurn                 context.CancelFunc
	generation                 uint64
	toolStarted                map[uint64]time.Time
	active                     bool
	play                       *playback
	newOutput                  func(Config) (consoleOutput, error)
	output                     consoleOutput
	deadline                   time.Time
	stageStarted               time.Time
	reprompted                 bool
	wireTurn, firstWireTurn    uint32
	replayCache                []cachedReply
	replayPCM                  []int16
	replayCapture              bool
	queuedAudio                []speechClip
	generationDone             bool
	cloudStarted, workDeadline time.Time
}

// Run connects once to /voice. It never retries and never downloads assets.
func Run(ctx context.Context, address string, cfg Config, host Host, commands <-chan Command, emit func(State)) error {
	return runWithDependencies(ctx, address, cfg, host, commands, emit, productionDependencies())
}

func voiceURL(address string) (string, error) {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		address = "ws://" + address
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/" && u.Path != "/voice") {
		return "", errors.New("invalid device voice address")
	}
	u.Path = "/voice"
	return u.String(), nil
}

func runWithDependencies(ctx context.Context, address string, cfg Config, host Host, commands <-chan Command, emit func(State), d dependencies) error {
	return runSession(ctx, address, cfg, host, commands, emit, d, false)
}

func runSession(ctx context.Context, address string, cfg Config, host Host, commands <-chan Command, emit func(State), d dependencies, standalone bool) (retErr error) {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return err
	}
	if emit == nil {
		emit = func(State) {}
	}
	if standalone {
		cfg.PlaybackOutput = "console"
	}
	s := &session{standalone: standalone, cfg: cfg, emit: emit, state: State{Phase: "idle"}, jobs: make(chan work, 1), results: make(chan result, 16), responses: make(chan result, 1), newOutput: d.newOutput}
	defer s.closeOutput()
	defer func() {
		status := "canceled"
		if retErr != nil && !errors.Is(retErr, context.Canceled) {
			status = "failed"
		}
		s.finishTools(status)
		s.state.Connected = false
		s.state.WakeReady = false
		if retErr != nil && !errors.Is(retErr, context.Canceled) {
			s.state.Error = s.safe(retErr.Error())
			s.state.Phase = "error"
		}
		s.emitState()
	}()
	if !cfg.Enabled {
		s.state.Phase = "disabled"
		return nil
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	s.ctx = sessionCtx
	defer cancel()
	var rx <-chan incoming
	var readerDone <-chan struct{}
	if !standalone {
		rx, readerDone, err = s.connectDevice(address)
		if err != nil {
			return err
		}
		defer func() { cancel(); s.ws.CloseNow(); <-readerDone }()
	}
	// Keep capture disabled while native models are loading.
	s.detector, err = d.newDetector(cfg)
	if err != nil {
		s.diagnostic = "Offline wake unavailable: " + s.safe(err.Error())
	}
	if s.detector != nil {
		defer s.detector.Close()
	}
	s.vad, err = d.newVAD(cfg)
	if err != nil {
		if s.diagnostic != "" {
			s.diagnostic += "; "
		}
		s.diagnostic += "VAD unavailable: " + s.safe(err.Error())
	}
	if s.vad != nil {
		defer s.vad.Close()
	}
	var input audioinput.Input
	var mic <-chan audioinput.Frame
	if standalone {
		if d.newInput == nil {
			return errors.New("本机麦克风不可用")
		}
		input, err = d.newInput(sessionCtx)
		if err != nil {
			s.state.ErrorStage = "listening"
			return err
		}
		if input == nil {
			return errors.New("本机麦克风不可用")
		}
		defer input.Close()
		mic = input.Frames()
		if mic == nil {
			s.state.ErrorStage = "listening"
			return errors.New("本机麦克风没有音频流")
		}
	}
	if err = sessionCtx.Err(); err != nil {
		return err
	}
	if err = s.send(message{Type: "config", Enabled: true, Volume: cfg.Volume}); err != nil {
		return err
	}
	s.state.Connected = true
	s.state.WakeReady = s.detector != nil && s.vad != nil
	s.state.Error = s.diagnostic
	s.state.ErrorStage = ""
	if err = s.phase("idle"); err != nil {
		return err
	}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		workerWithResponses(sessionCtx, cfg, host, d, s.jobs, s.results, s.responses)
	}()
	defer func() {
		// Stop host audio before waiting for cloud workers to acknowledge cancel.
		s.closeOutput()
		if s.cancelTurn != nil {
			s.cancelTurn()
		}
		cancel()
		if input != nil {
			input.Close()
		}
		if s.ws != nil {
			s.ws.CloseNow()
		}
		<-workerDone
	}()
	tickInterval := 250 * time.Millisecond
	if cfg.PlaybackOutput == "console" {
		tickInterval = 20 * time.Millisecond
	}
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	var pingC <-chan time.Time
	if !standalone {
		pingC = ping.C
	}
	pingErr := make(chan error, 1)
	pingActive := false
	for {
		results := s.results
		if s.streamQueueFull() {
			results = nil
		}
		select {
		case <-sessionCtx.Done():
			return sessionCtx.Err()
		case <-pingC:
			if !pingActive {
				pingActive = true
				go func() {
					c, done := context.WithTimeout(sessionCtx, 3*time.Second)
					defer done()
					e := s.ws.Ping(c)
					select {
					case pingErr <- e:
					case <-sessionCtx.Done():
					}
				}()
			}
		case e := <-pingErr:
			pingActive = false
			if e != nil {
				return errors.New("voice ping timeout")
			}
		case <-tick.C:
			if s.cfg.PlaybackOutput == "console" && s.play != nil {
				if err = s.pumpConsole(); err != nil {
					return err
				}
			}
			if err = s.recordingTimeout(time.Now()); err != nil {
				return err
			}
			if s.active && !s.generationDone && !s.workDeadline.IsZero() && time.Now().After(s.workDeadline.Add(stageGrace)) {
				if err = s.fail("timeout", errors.New("voice generation exceeded turn deadline")); err != nil {
					return err
				}
			}
			if s.active && !s.deadline.IsZero() && time.Now().After(s.deadline) {
				if err = s.fail("timeout", errors.New("voice operation timed out")); err != nil {
					return err
				}
			}
		case cmd, ok := <-commands:
			if !ok {
				commands = nil
				continue
			}
			if err = s.command(cmd); err != nil {
				return err
			}
		case r := <-s.responses:
			if err = s.result(r); err != nil {
				return err
			}
		case r := <-results:
			if err = s.flushResponse(); err != nil {
				return err
			}
			if err = s.result(r); err != nil {
				return err
			}
		case frame, ok := <-mic:
			if sessionCtx.Err() != nil {
				return sessionCtx.Err()
			}
			if !ok {
				s.state.ErrorStage = "listening"
				return errors.New("本机麦克风采集已停止")
			}
			if frame.Err != nil {
				s.state.ErrorStage = "listening"
				return frame.Err
			}
			if len(frame.PCM) != frameSamples {
				s.state.ErrorStage = "listening"
				return errors.New("invalid local microphone frame")
			}
			if err = s.microphonePCM(frame.PCM); err != nil {
				return err
			}
		case in := <-rx:
			if in.err != nil {
				if sessionCtx.Err() != nil {
					return sessionCtx.Err()
				}
				return errors.New("voice connection closed")
			}
			if in.kind == websocket.MessageBinary {
				err = s.microphone(in.data)
			} else if in.kind == websocket.MessageText {
				err = s.control(in.data)
			} else {
				err = errors.New("unsupported voice websocket message")
			}
			if err != nil {
				return err
			}
		}
	}
}

func (s *session) safe(text string) string {
	for _, secret := range []string{s.cfg.MiMoAPIKey, s.cfg.LLMAPIKey, s.cfg.FishAPIKey, s.cfg.TavilyAPIKey, s.cfg.HAAccessToken} {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	if len(text) > 2048 {
		text = text[:2048]
	}
	return text
}
func (s *session) send(m message) error {
	if s.standalone {
		return s.ctx.Err()
	}
	m.V = protocolVersion
	m.Epoch = s.epoch
	if m.Turn != 0 && m.Turn == s.state.Turn {
		m.Turn = s.wireTurn
	}
	// Device captions are bounded independently of full desktop transcripts.
	m.Text = deviceCaption(m.Text)
	m.Message = deviceCaption(m.Message)
	var payload any = m
	if m.Type == "playback_start" && m.Streaming {
		payload = struct {
			message
			Samples int `json:"samples"`
		}{message: m, Samples: 0}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	if err = s.ws.Write(c, websocket.MessageText, b); err != nil {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		return errors.New("voice send failed")
	}
	return nil
}
func deviceCaption(text string) string {
	const limit = 512 // even JSON-escaped ASCII control characters fit the 4096-byte device frame
	if len(text) <= limit {
		return text
	}
	text = text[:limit-3]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "…"
}

func (s *session) phase(phase string, notice ...string) error {
	s.state.Phase = phase
	s.state.Notice = ""
	if len(notice) > 0 {
		s.state.Notice = notice[0]
	}
	if budget := phaseBudget(phase); budget > 0 {
		s.stageStarted = time.Now()
		s.deadline = s.stageStarted.Add(budget + stageGrace)
	}
	s.emitState()
	return s.send(message{Type: "state", Turn: s.state.Turn, State: phase, Text: s.state.Notice})
}
func (s *session) resetCapture() {
	s.capture = nil
	s.manualFixed = false
	s.recording = false
	s.ring.reset()
	s.kwsSamples = 0
	if s.detector != nil {
		s.detector.Reset()
	}
	if s.vad != nil {
		s.vad.Reset()
	}
}

// emitState transfers an owned snapshot to consumers on other goroutines.
func (s *session) emitState() {
	s.emit(s.state.Clone())
}

func (s *session) finishTools(status string) {
	for i := range s.state.ToolCalls {
		call := &s.state.ToolCalls[i]
		if call.Status == "running" {
			call.Status = status
			call.Millis = max(1, call.Millis)
			if status == "failed" {
				call.Result = "工具调用失败"
			} else {
				call.Result = "工具调用已取消"
			}
			if started, ok := s.toolStarted[call.ID]; ok {
				call.Millis = max(1, call.Millis, time.Since(started).Milliseconds())
			}
		}
	}
	s.toolStarted = nil
}

func (s *session) updateTool(call brain.ToolCall) {
	if call.ID == 0 {
		return
	}
	for i := range s.state.ToolCalls {
		if s.state.ToolCalls[i].ID == call.ID {
			if s.state.ToolCalls[i].Status != "running" {
				return
			}
			s.state.ToolCalls[i] = call
			if call.Status != "running" {
				delete(s.toolStarted, call.ID)
			}
			s.emitState()
			return
		}
	}
	// Keep the first 32 calls so later completion events cannot reinsert an
	// evicted call or lose an earlier running entry.
	if len(s.state.ToolCalls) >= 32 {
		return
	}
	s.state.ToolCalls = append(s.state.ToolCalls, call)
	if call.Status == "running" {
		if s.toolStarted == nil {
			s.toolStarted = make(map[uint64]time.Time)
		}
		s.toolStarted[call.ID] = time.Now()
	}
	s.emitState()
}

func (s *session) abort(notify bool) error {
	s.finishTools("canceled")
	if s.cancelTurn != nil {
		s.cancelTurn()
		s.cancelTurn = nil
	}
	wasActive := s.active
	s.active = false
	s.closeOutput()
	s.play = nil
	s.replayPCM = nil
	s.replayCapture = false
	s.state.ReplayingTurn = 0
	s.state.ReplayingKey = ""
	s.queuedAudio = nil
	s.generationDone = false
	s.workDeadline = time.Time{}
	s.deadline = time.Time{}
	s.resetCapture()
	if wasActive && notify {
		return s.send(message{Type: "cancel", Turn: s.state.Turn})
	}
	return nil
}
func (s *session) begin() error {
	if err := s.abort(true); err != nil {
		return err
	}
	s.state.ToolCalls = nil
	s.state.Turn++
	if s.state.Turn == 0 {
		s.state.Turn = 1
	}
	if err := s.advanceWireTurn(); err != nil {
		return err
	}
	s.firstWireTurn = s.wireTurn
	s.reprompted = false
	s.state.FirstAudioMillis = 0
	s.state.GeneratedSegments = 0
	s.state.PlayedSegments = 0
	s.state.Transcript = ""
	s.state.Response = ""
	s.lastResponseWire = time.Time{}
	s.state.ASRMillis = 0
	s.state.AgentMillis = 0
	s.state.TTSMillis = 0
	s.state.Error = s.diagnostic
	s.state.ErrorStage = ""
	s.active = true
	s.deadline = time.Now().Add(cloudWorkBudget + stageGrace)
	return nil
}
func (s *session) submit(kind, text string, pcm []int16) {
	s.replayCapture = kind == "text" || kind == "utterance" || kind == "respeak"
	s.replayPCM = nil
	c, cancel := context.WithTimeout(s.ctx, cloudWorkBudget)
	s.cancelTurn = cancel
	s.cloudStarted = time.Now()
	s.workDeadline, _ = c.Deadline()
	w := work{ctx: c, turn: s.state.Turn, generation: s.generation, kind: kind, text: text, pcm: pcm}
	select {
	case <-s.jobs:
	default:
	}
	s.jobs <- w
}
func (s *session) command(c Command) error {
	switch c.Kind {
	case "replay":
		return s.replay(c.Turn)
	case "respeak":
		return s.respeak(c.Text)
	case "text":
		text := strings.TrimSpace(c.Text)
		if text == "" || len(c.Text) > 4096 {
			return nil
		}
		if err := s.begin(); err != nil {
			return err
		}
		s.state.Transcript = text
		if err := s.send(message{Type: "transcript", Turn: s.state.Turn, Text: text}); err != nil {
			return err
		}
		s.submit("text", text, nil)
		return s.phase("thinking")
	case "record_start":
		if s.muted || s.recording {
			return nil
		}
		if err := s.begin(); err != nil {
			return err
		}
		s.capture = &capture{}
		s.recording = true
		// A wake-only transcript must not reopen capture after the user releases.
		s.reprompted = true
		s.deadline = time.Now().Add(30 * time.Second)
		return s.phase("listening")
	case "record_stop":
		if !s.recording {
			return nil
		}
		return s.stopRecording()
	case "record_cancel":
		if !s.recording {
			return nil
		}
		return s.command(Command{Kind: "cancel"})
	case "wake":
		if s.muted {
			return nil
		}
		if err := s.begin(); err != nil {
			return err
		}
		if err := s.phase("wake"); err != nil {
			return err
		}
		s.capture = &capture{}
		s.manualFixed = s.vad == nil
		if s.manualFixed {
			s.state.Error = s.diagnostic + "; manual recording uses a fixed 6 second window"
		}
		s.deadline = time.Now().Add(40 * time.Second)
		return s.phase("listening")
	case "cancel":
		if err := s.abort(true); err != nil {
			return err
		}
		s.state.Error = s.diagnostic
		s.state.ErrorStage = ""
		return s.phase("idle")
	case "clear":
		if err := s.abort(true); err != nil {
			return err
		}
		s.generation++
		s.replayCache = nil
		s.state.ReplayTurns = nil
		s.state.ReplayKeys = nil
		s.state.ToolCalls = nil
		s.state.Transcript = ""
		s.state.Response = ""
		s.state.Error = s.diagnostic
		s.state.ErrorStage = ""
		s.submit("clear", "", nil)
		return s.phase("idle")
	case "preview", "reference":
		if err := s.begin(); err != nil {
			return err
		}
		if c.Kind == "preview" {
			// Keep the text available for another explicit retry if synthesis fails again.
			s.state.Response = strings.TrimSpace(c.Text)
		}
		s.submit(c.Kind, c.Text, nil)
		return s.phase("synthesizing")
	default:
		return nil
	}
}

// Worker stages take precedence over the visible phase: thinking and speech
// synthesis can overlap, so the UI phase alone cannot identify the failure.
func failureStage(code, phase string) string {
	switch code {
	case "recognizing", "thinking", "synthesizing", "wake":
		return code
	case "playback", "stream_start", "stream_audio", "stream_end", "bad_credit", "bad_played", "audio_size":
		return "speaking"
	case "bad_audio", "mic_gap", "vad":
		return "listening"
	}
	switch phase {
	case "connecting", "wake", "listening", "recognizing", "thinking", "synthesizing", "speaking":
		return phase
	default:
		return "unknown"
	}
}

func (s *session) fail(code string, err error) error {
	s.finishTools("failed")
	if phaseBudget(s.state.Phase) > 0 && !s.stageStarted.IsZero() {
		s.recordStageMillis(s.state.Phase, max(1, time.Since(s.stageStarted).Milliseconds()))
	}
	if e := s.abort(true); e != nil {
		return e
	}
	s.state.Error = s.safe(err.Error())
	s.state.ErrorStage = failureStage(code, s.state.Phase)
	if e := s.send(message{Type: "error", Turn: s.state.Turn, Code: code, Message: s.state.Error}); e != nil {
		return e
	}
	return s.phase("error")
}
func (s *session) microphone(b []byte) error {
	f, err := decodeAudio(b)
	if err != nil {
		return s.fail("bad_audio", err)
	}
	if f.Kind != kindMicrophone {
		return s.fail("bad_audio", errors.New("device sent non-microphone audio"))
	}
	if f.Epoch != s.epoch {
		return nil
	}
	if f.Seq != s.nextMic {
		s.nextMic = f.Seq + 1
		if s.capture != nil {
			return s.fail("mic_gap", errors.New("microphone sequence gap; recording canceled"))
		}
		s.resetCapture()
		return nil
	}
	s.nextMic++
	return s.microphonePCM(f.PCM)
}

// microphonePCM is shared by native input and validated device frames.
func (s *session) microphonePCM(pcm []int16) error {
	var err error
	// Without AEC, local speaker audio must never enter keyword spotting.
	if s.standalone && s.play != nil {
		return nil
	}
	if s.muted {
		return nil
	}
	// Hold recording deliberately bypasses both keyword spotting and VAD.
	if s.recording {
		remaining := maxUtteranceSamples - len(s.capture.pcm)
		s.capture.pcm = append(s.capture.pcm, pcm[:min(remaining, len(pcm))]...)
		if len(s.capture.pcm) >= maxUtteranceSamples {
			return s.stopRecording()
		}
		return nil
	}
	// Retain the detector decision delay during capture, cloud work and playback
	// too, so a wake word followed immediately by a new question loses no suffix.
	if s.state.WakeReady {
		s.ring.add(pcm)
		s.kwsSamples += int64(len(pcm))
		hit, err := s.detector.Feed(pcm)
		if err != nil {
			s.state.WakeReady = false
			s.diagnostic = "Offline wake unavailable: " + s.safe(err.Error())
			return s.fail("wake", err)
		}
		if hit {
			end := s.detector.KeywordEndSamples()
			// Never include keyword audio without a valid detector endpoint.
			suffix := 0
			if end > 0 && end <= s.kwsSamples {
				suffix = int(s.kwsSamples - end)
			}
			// begin resets the detector and ring while canceling the old turn.
			p := s.ring.tail(suffix)
			if err = s.command(Command{Kind: "wake"}); err != nil {
				return err
			}
			for len(p) > 0 {
				n := min(frameSamples, len(p))
				speech, e := s.vad.Speech(p[:n])
				if e != nil {
					return s.fail("vad", e)
				}
				s.capture.feed(p[:n], speech)
				p = p[n:]
			}
			return nil
		}
	}
	if s.capture != nil {
		var speech bool
		if s.vad != nil {
			speech, err = s.vad.Speech(pcm)
			if err != nil {
				return s.fail("vad", err)
			}
		}
		done, empty := false, false
		if s.manualFixed {
			s.capture.pcm = append(s.capture.pcm, pcm...)
			s.capture.elapsed += len(pcm)
			done = s.capture.elapsed >= manualWindowSamples
		} else {
			done, empty = s.capture.feed(pcm, speech)
		}
		if done {
			if empty {
				return s.finishNoQuestion()
			}
			pcm := s.capture.pcm
			s.capture = nil
			s.manualFixed = false
			s.deadline = time.Now().Add(cloudWorkBudget + stageGrace)
			s.submit("utterance", "", pcm)
			return s.phase("recognizing")
		}
		return nil
	}
	return nil
}

// stopRecording consumes the capture before submitting so repeated release events
// cannot queue the same utterance twice. Empty holds never initialize cloud work.
func (s *session) stopRecording() error {
	pcm := s.capture.pcm
	s.capture = nil
	s.recording = false
	if len(pcm) == 0 {
		return s.finishNoQuestion()
	}
	s.deadline = time.Now().Add(cloudWorkBudget + stageGrace)
	s.submit("utterance", "", pcm)
	return s.phase("recognizing")
}

func (s *session) recordingTimeout(now time.Time) error {
	if s.recording && !now.Before(s.deadline) {
		return s.stopRecording()
	}
	return nil
}

func (s *session) control(b []byte) error {
	m, err := decodeMessage(b)
	if err != nil {
		return s.fail("bad_control", err)
	}
	if m.Epoch != s.epoch {
		return nil
	}
	switch m.Type {
	case "wake", "clear":
		return s.command(Command{Kind: m.Type})
	case "mute":
		s.muted = m.Muted
		if s.muted {
			return s.command(Command{Kind: "cancel"})
		}
		s.resetCapture()
		return nil
	case "cancel":
		if s.active && m.Turn >= s.firstWireTurn && m.Turn <= s.wireTurn {
			if err = s.abort(false); err != nil {
				return err
			}
			s.state.Error = s.diagnostic
			s.state.ErrorStage = ""
			return s.phase("idle")
		}
	case "credit":
		if s.cfg.PlaybackOutput == "console" {
			return nil
		}
		if s.play == nil || m.Turn != s.wireTurn {
			return nil
		}
		if m.Frames <= 0 || m.Frames > maxCredits || s.play.credits+m.Frames > maxCredits {
			return s.fail("bad_credit", errors.New("invalid playback credits"))
		}
		s.play.credits += m.Frames
		return s.pump()
	case "playing":
		if s.cfg.PlaybackOutput == "console" {
			return nil
		}
		if s.play != nil && m.Turn == s.wireTurn && s.play.seq > 0 && !s.play.playing {
			s.play.playing = true
			if s.state.FirstAudioMillis == 0 {
				s.state.FirstAudioMillis = max(1, time.Since(s.cloudStarted).Milliseconds())
			}
			return s.phase("speaking")
		}
	case "played":
		if s.cfg.PlaybackOutput == "console" {
			return nil
		}
		if s.play != nil && m.Turn == s.wireTurn {
			if !s.play.ended || !s.play.playing {
				return s.fail("bad_played", errors.New("playback acknowledgement arrived before rendered audio"))
			}
			s.state.PlayedSegments++
			s.play = nil
			return s.nextSegment(true)
		}
	case "error":
		if m.Turn == s.wireTurn || m.Turn == 0 {
			return s.fail("device", fmt.Errorf("device voice error: %s", s.safe(m.Message)))
		}
	case "hello":
		return errors.New("unexpected second voice hello")
	}
	return nil
}
func (s *session) finishNoQuestion() error {
	if err := s.abort(true); err != nil {
		return err
	}
	s.state.Error = s.diagnostic
	s.state.ErrorStage = ""
	return s.phase("idle", "没有听到问题，请重新唤醒后说出问题")
}

func (s *session) continueAfterWakeOnly(millis int64) error {
	s.state.ASRMillis = millis
	// At most one additional capture per turn. Repeated wake words must not
	// create an unbounded sequence of recognition requests.
	if s.reprompted {
		return s.finishNoQuestion()
	}
	s.reprompted = true
	if s.cancelTurn != nil {
		s.cancelTurn()
		s.cancelTurn = nil
	}
	s.resetCapture()
	s.capture = &capture{}
	s.manualFixed = s.vad == nil
	s.state.Error = s.diagnostic
	s.state.ErrorStage = ""
	s.deadline = time.Now().Add(40 * time.Second)
	return s.phase("listening", "我在听，请继续说出问题")
}

func (s *session) recordStageMillis(stage string, millis int64) {
	switch stage {
	case "recognizing":
		s.state.ASRMillis = max(s.state.ASRMillis, millis)
	case "thinking":
		s.state.AgentMillis = max(s.state.AgentMillis, millis)
	case "synthesizing":
		s.state.TTSMillis = max(s.state.TTSMillis, millis)
	}
}

func (s *session) result(r result) error {
	if r.turn != s.state.Turn || r.generation != s.generation || (!s.active && r.phase != "context") {
		return nil
	}
	if r.context != nil {
		s.state.Context = *r.context
	}
	if r.phase == "context" {
		s.emitState()
		return nil
	}
	if !s.active {
		return nil
	}
	if r.homeAssistant != nil {
		s.state.HomeAssistant = *r.homeAssistant
		s.state.HomeAssistant.Error = s.safe(s.state.HomeAssistant.Error)
		s.emitState()
		return nil
	}
	if r.toolCall != nil {
		s.updateTool(*r.toolCall)
		return nil
	}
	if r.err != nil {
		s.recordStageMillis(r.phase, r.millis)
		return s.fail(r.phase, r.err)
	}
	switch r.phase {
	case "wake_only":
		return s.continueAfterWakeOnly(r.millis)
	case "transcript":
		s.state.Transcript = r.text
		s.state.ASRMillis = r.millis
		s.emitState()
		return s.send(message{Type: "transcript", Turn: r.turn, Text: r.text})
	case "response":
		s.state.Response = r.text
		s.state.AgentMillis = r.millis
		s.emitState()
		// Desktop snapshots update immediately; avoid flooding the device with
		// a JSON message per token. Always deliver the authoritative final text.
		if !r.final && time.Since(s.lastResponseWire) < responseWireInterval {
			return nil
		}
		s.lastResponseWire = time.Now()
		return s.send(message{Type: "response", Turn: r.turn, Text: r.text})
	case "audio_stream_start":
		return s.startAudioStream()
	case "audio_stream_chunk":
		s.state.TTSMillis = r.millis
		return s.appendAudioStream(r.pcm)
	case "audio_stream_end":
		s.state.TTSMillis = r.millis
		return s.finishAudioStream(r.samples)
	case "generation_done":
		s.commitReplay()
		s.generationDone = true
		return s.nextSegment(false)
	case "audio_segment":
		s.captureReplay(r.pcm)
		if r.final {
			s.commitReplay()
		}
		s.generationDone = r.final
		s.state.TTSMillis = r.millis
		s.state.GeneratedSegments++
		s.queuedAudio = append(s.queuedAudio, speechClip{pcm: r.pcm, text: r.text})
		s.emitState()
		return s.nextSegment(false)
	case "audio":
		s.generationDone = true
		s.state.GeneratedSegments = 1
		if len(r.pcm) == 0 || len(r.pcm) > maxPlaybackSamples {
			return s.fail("audio_size", errors.New("empty or oversized playback audio"))
		}
		s.captureReplay(r.pcm)
		s.commitReplay()
		s.state.TTSMillis = r.millis
		s.play = &playback{pcm: r.pcm}
		s.deadline = time.Now().Add(time.Duration(len(r.pcm))*time.Second/sampleRate + 15*time.Second)
		return s.startPlayback(false, len(r.pcm))
	default:
		if s.play != nil && (r.phase == "thinking" || r.phase == "synthesizing") {
			return nil
		}
		return s.phase(r.phase)
	}
}
func (s *session) pump() error {
	if s.cfg.PlaybackOutput == "console" {
		return s.pumpConsole()
	}
	p := s.play
	for p.credits > 0 && p.offset < len(p.pcm) {
		n := min(frameSamples, len(p.pcm)-p.offset)
		if p.streaming && !p.inputEnded && n < frameSamples {
			break
		}
		b := encodeAudio(audioFrame{Kind: kindPlayback, Epoch: s.epoch, Turn: s.wireTurn, Seq: p.seq, PCM: p.pcm[p.offset : p.offset+n]})
		c, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		err := s.ws.Write(c, websocket.MessageBinary, b)
		cancel()
		if err != nil {
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			return errors.New("voice audio send failed")
		}
		p.offset += n
		p.seq++
		p.credits--
	}
	if p.offset == len(p.pcm) && !p.ended && (!p.streaming || p.inputEnded) {
		p.ended = true
		end := message{Type: "playback_end", Turn: s.state.Turn}
		if p.streaming {
			end.Samples = p.received
		}
		return s.send(end)
	}
	return nil
}
