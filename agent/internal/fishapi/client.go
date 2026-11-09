// Package fishapi implements Fish Audio's MessagePack bidirectional TTS protocol.
package fishapi

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"aemeath/agent/internal/voiceapi"
	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	sampleRate      = 16000
	maxSamples      = sampleRate * 120
	maxTextBytes    = 16 << 10
	maxMessageBytes = 1 << 20
	writeTimeout    = 10 * time.Second
	readTimeout     = 30 * time.Second
)

type Config struct{ BaseURL, APIKey, Model, ReferenceID, ReferenceAudio, ReferenceText, Latency string }

// Client is immutable after New and may run independent streams concurrently.
type Client struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.fish.audio"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Fish: invalid base URL")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return nil, errors.New("Fish: invalid base URL")
	}
	switch u.Path {
	case "", "/":
		u.Path = "/v1/tts/live"
	case "/v1":
		u.Path = "/v1/tts/live"
	default:
		if !strings.HasSuffix(u.Path, "/v1/tts/live") {
			return nil, errors.New("Fish: base URL must point to /v1/tts/live")
		}
	}
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" || len(cfg.APIKey) > 4096 || strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("Fish: API key is required and must be a single line")
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" {
		cfg.Model = "s2.1-pro"
	}
	if len(cfg.Model) > 128 || strings.ContainsAny(cfg.Model, "\r\n") {
		return nil, errors.New("Fish: invalid model")
	}
	cfg.Latency = strings.TrimSpace(cfg.Latency)
	if cfg.Latency == "" {
		cfg.Latency = "balanced"
	}
	switch cfg.Latency {
	case "normal", "balanced", "low":
	default:
		return nil, errors.New("Fish: invalid latency mode")
	}
	cfg.ReferenceID = strings.TrimSpace(cfg.ReferenceID)
	if len(cfg.ReferenceID) > 512 || !utf8.ValidString(cfg.ReferenceID) {
		return nil, errors.New("Fish: invalid reference ID")
	}
	if cfg.ReferenceID == "" {
		cfg.ReferenceAudio = strings.TrimSpace(cfg.ReferenceAudio)
		if cfg.ReferenceAudio == "" || strings.TrimSpace(cfg.ReferenceText) == "" || len(cfg.ReferenceText) > maxTextBytes || !utf8.ValidString(cfg.ReferenceText) {
			return nil, errors.New("Fish: provide a reference ID or a WAV sample with its exact transcript")
		}
	}
	return &Client{cfg: cfg, endpoint: u.String(), http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type reference struct {
	Audio []byte `msgpack:"audio"`
	Text  string `msgpack:"text"`
}
type request struct {
	Text        string      `msgpack:"text"`
	Format      string      `msgpack:"format"`
	SampleRate  int         `msgpack:"sample_rate"`
	Latency     string      `msgpack:"latency"`
	ReferenceID string      `msgpack:"reference_id,omitempty"`
	References  []reference `msgpack:"references,omitempty"`
}
type event struct {
	Event   string   `msgpack:"event"`
	Text    string   `msgpack:"text,omitempty"`
	Request *request `msgpack:"request,omitempty"`
}

func (c *Client) startRequest(ctx context.Context) (*request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := &request{Text: "", Format: "pcm", SampleRate: sampleRate, Latency: c.cfg.Latency, ReferenceID: c.cfg.ReferenceID}
	if c.cfg.ReferenceID != "" {
		return r, nil
	}
	// Preserve the exact supplied transcript. There is no transcription or model upload side effect.
	pcm, err := voiceapi.ReferencePCM(c.cfg.ReferenceAudio)
	if err != nil {
		return nil, errors.New("Fish: invalid reference WAV")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wav := voiceapi.EncodeWAV(pcm, sampleRate)
	if len(wav) == 0 || len(wav) > 4<<20 {
		return nil, errors.New("Fish: empty or oversized reference WAV")
	}
	r.References = []reference{{Audio: wav, Text: c.cfg.ReferenceText}}
	return r, nil
}

// Stream starts one session, sends input and receives audio concurrently, and never
// retries or replays accepted text. onAudio runs synchronously to apply backpressure;
// callers must arrange for a blocked callback to return when their context is canceled.
// Each callback owns its fresh PCM slice. A valid finish is required after input EOF/stop.
func (c *Client) Stream(parent context.Context, text <-chan string, onAudio func([]int16) error) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if text == nil || onAudio == nil {
		return errors.New("Fish: text channel and audio callback are required")
	}
	start, err := c.startRequest(parent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	dialCtx, stopDial := context.WithTimeout(ctx, 10*time.Second)
	conn, res, err := websocket.Dial(dialCtx, c.endpoint, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.cfg.APIKey}, "Model": []string{c.cfg.Model}}, CompressionMode: websocket.CompressionDisabled})
	stopDial()
	if err != nil {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		return errors.New("Fish: WebSocket connection failed")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)
	if err = c.write(ctx, conn, event{Event: "start", Request: start}); err != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		return err
	}
	writerDone := make(chan struct{})
	var writerErr error
	var stopAttempted atomic.Bool
	go func() {
		writerErr = c.sendText(ctx, conn, text, &stopAttempted)
		// Publish the cause before cancellation wakes the reader.
		close(writerDone)
		if writerErr != nil {
			cancel()
			conn.CloseNow()
		}
	}()
	writerJoined := false
	defer func() {
		cancel()
		conn.CloseNow()
		if !writerJoined {
			<-writerDone
		}
	}()
	total := 0
	var pending byte
	havePending := false
	for {
		readCtx, stopRead := context.WithTimeout(ctx, readTimeout)
		kind, data, readErr := conn.Read(readCtx)
		stopRead()
		if readErr != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			select {
			case <-writerDone:
				writerJoined = true
				if writerErr != nil {
					return writerErr
				}
			default:
			}
			return errors.New("Fish: stream ended without successful finish")
		}
		if kind != websocket.MessageBinary {
			return errors.New("Fish: expected binary MessagePack event")
		}
		received, err := decodeEvent(data)
		if err != nil {
			return errors.New("Fish: invalid or oversized MessagePack event")
		}
		switch received.event {
		case "audio":
			audio := received.audio
			if len(audio) == 0 {
				continue
			}
			count := (len(audio) + btoi(havePending)) / 2
			if count > maxSamples-total {
				return errors.New("Fish: audio exceeds 120 second limit")
			}
			pcm := make([]int16, 0, count)
			pos := 0
			if havePending {
				pcm = append(pcm, int16(uint16(pending)|uint16(audio[0])<<8))
				pos = 1
				havePending = false
			}
			for pos+1 < len(audio) {
				pcm = append(pcm, int16(binary.LittleEndian.Uint16(audio[pos:])))
				pos += 2
			}
			if pos < len(audio) {
				pending = audio[pos]
				havePending = true
			}
			total += len(pcm)
			if len(pcm) > 0 {
				if err = onAudio(pcm); err != nil {
					if parent.Err() != nil {
						return parent.Err()
					}
					return errors.New("Fish: audio consumer stopped")
				}
			}
			if parent.Err() != nil {
				return parent.Err()
			}
		case "finish":
			if received.reason != "stop" {
				return errors.New("Fish: synthesis failed")
			}
			if !stopAttempted.Load() {
				return errors.New("Fish: premature finish before text stream completed")
			}
			// The peer may reply before Write(stop) has returned locally; join its result.
			<-writerDone
			writerJoined = true
			if writerErr != nil {
				return writerErr
			}
			if havePending {
				return errors.New("Fish: truncated PCM sample at finish")
			}
			if total == 0 {
				return errors.New("Fish: no audio received")
			}
			if parent.Err() != nil {
				return parent.Err()
			}
			return nil
		case "error":
			return errors.New("Fish: provider reported an error")
		default: // Official protocol asks clients to ignore unknown extension events.
		}
	}
}
func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (c *Client) write(ctx context.Context, conn *websocket.Conn, v event) error {
	data, err := msgpack.Marshal(v)
	if err != nil {
		return errors.New("Fish: cannot encode request")
	}
	sendCtx, stop := context.WithTimeout(ctx, writeTimeout)
	defer stop()
	if conn.Write(sendCtx, websocket.MessageBinary, data) != nil {
		return errors.New("Fish: WebSocket send failed")
	}
	return nil
}
func (c *Client) sendText(ctx context.Context, conn *websocket.Conn, text <-chan string, stopAttempted *atomic.Bool) error {
	total := 0
	var partial string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delta, ok := <-text:
			if !ok {
				if partial != "" {
					return errors.New("Fish: incomplete UTF-8 text")
				}
				if total == 0 {
					return errors.New("Fish: empty text stream")
				}
				stopAttempted.Store(true)
				return c.write(ctx, conn, event{Event: "stop"})
			}
			if len(delta) > maxTextBytes-total {
				return errors.New("Fish: text exceeds 16384 byte limit")
			}
			total += len(delta)
			raw := partial + delta
			complete := 0
			for complete < len(raw) {
				if !utf8.FullRuneInString(raw[complete:]) {
					break
				}
				r, n := utf8.DecodeRuneInString(raw[complete:])
				if r == utf8.RuneError && n == 1 {
					return errors.New("Fish: invalid UTF-8 text")
				}
				complete += n
			}
			partial = raw[complete:]
			raw = raw[:complete]
			if raw == "" {
				continue
			}
			if err := c.write(ctx, conn, event{Event: "text", Text: raw}); err != nil {
				return err
			}
			if err := c.write(ctx, conn, event{Event: "flush"}); err != nil {
				return err
			}
		}
	}
}
