package voiceapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 24 << 20

// Client is safe for concurrent use. No request or response body is included in errors.
type Client struct {
	endpoint, key string
	http          *http.Client
	configErr     error
}

func New(baseURL, key string) *Client {
	c := &Client{key: strings.TrimSpace(key), http: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	wav := EncodeWAV(pcm, sampleRate)
	if wav == nil {
		return "", errors.New("MiMo ASR: audio must contain 1 to 1920000 PCM16 samples")
	}
	req := map[string]any{"model": "mimo-v2.5-asr", "stream": false, "asr_options": map[string]string{"language": "zh"}, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": dataURL(wav), "format": "wav"}}}}}}
	out, err := c.complete(ctx, req)
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
	out, err := c.complete(ctx, req)
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

func (c *Client) complete(ctx context.Context, payload any) (*completion, error) {
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
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("MiMo: invalid request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.key)
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("MiMo: HTTP request failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
		res.Body.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			return nil, errors.New("MiMo: could not read response")
		}
		if len(raw) > maxResponseBytes {
			return nil, errors.New("MiMo: response exceeds size limit")
		}
		if (res.StatusCode == 429 || res.StatusCode == 503) && attempt < 2 {
			delay := time.Duration(attempt+1) * 250 * time.Millisecond
			retryAfter := res.Header.Get("Retry-After")
			if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
				if seconds > 2 {
					return nil, fmt.Errorf("MiMo: HTTP %d; retry later", res.StatusCode)
				}
				delay = time.Duration(seconds) * time.Second
			} else if date, err := http.ParseTime(retryAfter); err == nil && time.Until(date) > delay {
				delay = time.Until(date)
			}
			if delay > 2*time.Second {
				return nil, fmt.Errorf("MiMo: HTTP %d; retry later", res.StatusCode)
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
			return nil, fmt.Errorf("MiMo: HTTP %d", res.StatusCode)
		}
		var out completion
		if json.Unmarshal(raw, &out) != nil {
			return nil, errors.New("MiMo: invalid JSON response")
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
