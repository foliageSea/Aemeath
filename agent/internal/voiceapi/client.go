package voiceapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes = 24 << 20

	// Budgets include upload, provider generation, response transfer, and decoding.
	// Batch clone synthesis is not a streaming response and can exceed one minute.
	// Callers may impose a shorter deadline; retries never reset these budgets.
	ASRRequestTimeout   = 60 * time.Second
	TTSRequestTimeout   = 180 * time.Second
	connectTimeout      = 10 * time.Second
	responseBodyTimeout = TTSRequestTimeout
)

var errResponseBodyTimeout = errors.New("response body timeout")

// Client is safe for concurrent use. No request or response body is included in errors.
type Client struct {
	endpoint, key string
	http          *http.Client
	configErr     error
	// Immutable after construction; separate fields permit short offline deadline tests.
	asrTimeout, ttsTimeout, bodyTimeout time.Duration
}

func New(baseURL, key string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = connectTimeout
	transport.ResponseHeaderTimeout = TTSRequestTimeout
	c := &Client{
		key: strings.TrimSpace(key),
		// A shared 60s http.Client.Timeout also cuts off a partly downloaded TTS WAV.
		// Operation contexts below supply bounded, phase-specific overall deadlines.
		http:       &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		asrTimeout: ASRRequestTimeout, ttsTimeout: TTSRequestTimeout, bodyTimeout: responseBodyTimeout,
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.xiaomimimo.com/v1"
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		c.configErr = errors.New("MiMo: invalid base URL")
		return c
	}
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	}
	c.endpoint = u.String()
	return c
}

type completion struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Audio   struct {
				Data string `json:"data"`
			} `json:"audio"`
		} `json:"message"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

func (c *Client) Recognize(ctx context.Context, pcm []int16) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.asrTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	wav := EncodeWAV(pcm, sampleRate)
	if wav == nil {
		return "", errors.New("MiMo ASR: audio must contain 1 to 1920000 PCM16 samples")
	}
	req := map[string]any{"model": "mimo-v2.5-asr", "stream": false, "asr_options": map[string]string{"language": "zh"}, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": dataURL(wav), "format": "wav"}}}}}}
	out, err := c.complete(ctx, req, "ASR")
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("MiMo ASR: empty transcript")
	}
	if len(text) > 64<<10 {
		return "", errors.New("MiMo ASR: transcript too large")
	}
	return text, nil
}

func (c *Client) Synthesize(ctx context.Context, text, referencePath string) ([]int16, error) {
	ctx, cancel := context.WithTimeout(ctx, c.ttsTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 16<<10 {
		return nil, errors.New("MiMo TTS: text must contain 1 to 16384 bytes")
	}
	reference, err := referencePCM(ctx, referencePath)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Re-encode the validated 24-bit/48 kHz reference as portable PCM16 WAV.
	req := map[string]any{"model": "mimo-v2.5-tts-voiceclone", "stream": false, "messages": []any{map[string]any{"role": "assistant", "content": text}}, "audio": map[string]string{"format": "wav", "voice": dataURL(EncodeWAV(reference, sampleRate))}}
	out, err := c.complete(ctx, req, "TTS")
	if err != nil {
		return nil, err
	}
	encoded := out.Choices[0].Message.Audio.Data
	if strings.HasPrefix(encoded, "data:") {
		const prefix = "data:audio/wav;base64,"
		if !strings.HasPrefix(encoded, prefix) {
			return nil, errors.New("MiMo TTS: expected WAV audio data")
		}
		encoded = strings.TrimPrefix(encoded, prefix)
	}
	if encoded == "" || base64.StdEncoding.DecodedLen(len(encoded)) > maxWAVBytes {
		return nil, errors.New("MiMo TTS: missing or oversized audio")
	}
	wav, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, errors.New("MiMo TTS: invalid base64 audio")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return decodeWAVContext(ctx, wav)
}

func dataURL(wav []byte) string {
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav)
}

// requestFailure never formats a transport error: url.Error, proxy failures, and
// custom transports can include the endpoint, credentials, or request content.
func requestFailure(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("MiMo %s: request ended before response headers: %w", operation, ctx.Err())
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return fmt.Errorf("MiMo %s: timeout before response headers; request not retried: %w", operation, context.DeadlineExceeded)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("MiMo %s: connection closed before response headers; request not retried", operation)
	}
	return fmt.Errorf("MiMo %s: network request failed before response headers; request not retried", operation)
}

func (c *Client) readResponse(ctx context.Context, cancel context.CancelCauseFunc, res *http.Response, operation string) ([]byte, error) {
	// The total operation deadline still wins. A phase cap also protects a body
	// returned by a custom transport; cancellation must unblock its Read/Close.
	timer := time.AfterFunc(c.bodyTimeout, func() { cancel(errResponseBodyTimeout) })
	var closeOnce sync.Once
	closeBody := func() { closeOnce.Do(func() { _ = res.Body.Close() }) }
	stopClose := context.AfterFunc(ctx, closeBody)
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	timer.Stop()
	stopClose()
	closeBody()
	failure := func(description string, cause error) ([]byte, error) {
		message := fmt.Sprintf("MiMo %s: %s (HTTP %d, received %d bytes)", operation, description, res.StatusCode, len(raw))
		if cause != nil {
			return nil, fmt.Errorf("%s: %w", message, cause)
		}
		return nil, errors.New(message)
	}
	if cause := context.Cause(ctx); cause != nil {
		if errors.Is(cause, errResponseBodyTimeout) || errors.Is(cause, context.DeadlineExceeded) {
			return failure("response body timed out; request not retried", context.DeadlineExceeded)
		}
		return failure("response body canceled; request not retried", context.Canceled)
	}
	if err != nil {
		var timeout net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			return failure("response body timed out; request not retried", context.DeadlineExceeded)
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || (res.ContentLength >= 0 && int64(len(raw)) < res.ContentLength) {
			return failure("truncated response body; request not retried", io.ErrUnexpectedEOF)
		}
		return failure("connection interrupted while reading response body; request not retried", nil)
	}
	if res.ContentLength >= 0 && int64(len(raw)) < res.ContentLength && len(raw) <= maxResponseBytes {
		return failure("truncated response body; request not retried", io.ErrUnexpectedEOF)
	}
	return raw, nil
}

func (c *Client) complete(ctx context.Context, payload any, operation string) (*completion, error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	if c.key == "" {
		return nil, errors.New("MiMo: API key is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("MiMo: cannot encode request")
	}
	// Retry only explicit rejection (429/503). Never replay ambiguous transport failures,
	// successful-but-invalid responses, authentication errors, or synthesis already accepted.
	for attempt := 0; attempt < 3; attempt++ {
		requestCtx, stopRequest := context.WithCancelCause(ctx)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			stopRequest(nil)
			return nil, errors.New("MiMo: invalid request")
		}
		// This paid POST must not be automatically replayed after an ambiguous write.
		req.GetBody = nil
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.key)
		res, err := c.http.Do(req)
		if err != nil {
			stopRequest(nil)
			return nil, requestFailure(ctx, operation, err)
		}
		raw, readErr := c.readResponse(requestCtx, stopRequest, res, operation)
		stopRequest(nil)
		if readErr != nil {
			return nil, readErr
		}
		if len(raw) > maxResponseBytes {
			return nil, errors.New("MiMo: response exceeds size limit")
		}
		if (res.StatusCode == 429 || res.StatusCode == 503) && attempt < 2 {
			delay := time.Duration(attempt+1) * 250 * time.Millisecond
			retryAfter := res.Header.Get("Retry-After")
			if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
				if seconds > 2 {
					return nil, fmt.Errorf("MiMo %s: HTTP %d; retry later", operation, res.StatusCode)
				}
				delay = time.Duration(seconds) * time.Second
			} else if date, err := http.ParseTime(retryAfter); err == nil && time.Until(date) > delay {
				delay = time.Until(date)
			}
			if delay > 2*time.Second {
				return nil, fmt.Errorf("MiMo %s: HTTP %d; retry later", operation, res.StatusCode)
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, fmt.Errorf("MiMo %s: HTTP %d", operation, res.StatusCode)
		}
		var out completion
		if json.Unmarshal(raw, &out) != nil {
			return nil, fmt.Errorf("MiMo %s: invalid or truncated JSON response (HTTP %d, received %d bytes)", operation, res.StatusCode, len(raw))
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("MiMo %s: response decoding interrupted: %w", operation, err)
		}
		if len(out.Error) > 0 && string(out.Error) != "null" {
			return nil, errors.New("MiMo: provider rejected request")
		}
		if len(out.Choices) == 0 {
			return nil, errors.New("MiMo: response has no choices")
		}
		return &out, nil
	}
	return nil, errors.New("MiMo: retry limit reached")
}
