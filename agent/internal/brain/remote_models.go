package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"aemeath/agent/internal/netproxy"
)

const (
	remoteModelsTimeout  = 15 * time.Second
	remoteModelsMaxBytes = 4 << 20
)

// FetchRemoteModels lists the model IDs advertised by an OpenAI-compatible
// provider. An empty baseURL uses OpenAI; an empty apiKey permits local servers.
// Requests use the context's network router, never follow redirects, and bound
// both the complete request duration and the decompressed response size.
func FetchRemoteModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	endpoint, err := remoteModelsEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, remoteModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("remote models: invalid request")
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{
		Transport:     netproxy.Transport(nil),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, remoteModelsRequestError(ctx, "request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// Never include the status text, URL, or response body: all are untrusted
		// and may contain credentials echoed by the provider or proxy.
		return nil, fmt.Errorf("remote models: HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > remoteModelsMaxBytes {
		return nil, errors.New("remote models: response too large")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, remoteModelsMaxBytes+1))
	if err != nil {
		return nil, remoteModelsRequestError(ctx, "response unreadable")
	}
	if len(body) > remoteModelsMaxBytes {
		return nil, errors.New("remote models: response too large")
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Data == nil {
		return nil, errors.New("remote models: invalid response format")
	}
	seen := make(map[string]struct{}, len(payload.Data))
	models := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models, nil
}

func remoteModelsEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(baseURL, "#") {
		return "", errors.New("remote models: base URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	return strings.TrimRight(baseURL, "/") + "/models", nil
}

func remoteModelsRequestError(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("remote models: %w", err)
	}
	// Do not wrap transport errors: they may contain URLs, proxy credentials,
	// invalid authorization header values, or raw remote response text.
	return fmt.Errorf("remote models: %s", message)
}
