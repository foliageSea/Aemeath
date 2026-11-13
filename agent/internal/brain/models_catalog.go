package brain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aemeath/agent/internal/netproxy"
)

const modelsCatalogURL = "https://models.dev/api.json"
const modelsCatalogMaxBytes = 32 << 20
const modelsCatalogTTL = 24 * time.Hour
const modelsCatalogRetry = 5 * time.Minute
const modelsCatalogTimeout = 5 * time.Second

type catalogModel struct {
	ID    string `json:"id"`
	Limit struct {
		Context int `json:"context"`
		Input   int `json:"input,omitempty"`
	} `json:"limit"`
}

type catalogProvider struct {
	API    string                  `json:"api,omitempty"`
	Models map[string]catalogModel `json:"models"`
}

type catalogFile struct {
	Version   int                        `json:"version"`
	UpdatedAt time.Time                  `json:"updated_at"`
	Providers map[string]catalogProvider `json:"providers"`
}

type catalogMatch struct {
	window, input int
	reference     string
}

// ModelCatalog contains public metadata only. Matching happens locally, and
// downloads never include a configured service URL, model ID or credentials.
type ModelCatalog struct {
	mu            sync.RWMutex
	path          string
	endpoint      string
	providers     map[string]catalogProvider
	updated       time.Time
	attempted     time.Time
	refreshing    chan struct{}
	refreshCancel context.CancelFunc
}

func NewModelCatalog(path string) *ModelCatalog {
	c := &ModelCatalog{path: path, endpoint: modelsCatalogURL}
	if path == "" {
		return c
	}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, modelsCatalogMaxBytes+1))
	var stored catalogFile
	if err == nil && len(data) <= modelsCatalogMaxBytes && json.Unmarshal(data, &stored) == nil && stored.Version == 1 && !stored.UpdatedAt.IsZero() && !stored.UpdatedAt.After(time.Now()) && usableCatalog(stored.Providers) {
		c.providers, c.updated = stored.Providers, stored.UpdatedAt
	}
	return c
}

// DefaultModelCatalog shares the CLI's cache across voice reconnects. Desktop
// services own a separate catalog next to their preferences.
func DefaultModelCatalog() *ModelCatalog { return defaultModelCatalog() }

var defaultModelCatalog = sync.OnceValue(func() *ModelCatalog {
	path := ""
	if root, err := os.UserCacheDir(); err == nil {
		path = filepath.Join(root, "Aemeath", "models_dev_cache.json")
	}
	return NewModelCatalog(path)
})

// RefreshAsync coalesces downloads and returns immediately. Stale data remains
// usable while refreshing or offline; failures are retried no sooner than 5m.
func (c *ModelCatalog) RefreshAsync(parent context.Context) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshAsyncLocked(parent)
}

// RefreshAfterNetworkChange retries stale metadata immediately on a new route,
// without evicting a fresh valid cache. Superseded downloads cannot publish data
// or clear the replacement job; their existing waiters still receive completion.
func (c *ModelCatalog) RefreshAfterNetworkChange(parent context.Context) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshCancel != nil {
		c.refreshCancel()
		c.refreshCancel = nil
	}
	c.refreshing = nil
	c.attempted = time.Time{}
	return c.refreshAsyncLocked(parent)
}

func (c *ModelCatalog) refreshAsyncLocked(parent context.Context) <-chan struct{} {
	if c.refreshing != nil {
		return c.refreshing
	}
	if parent.Err() != nil || (!c.updated.IsZero() && time.Since(c.updated) < modelsCatalogTTL) || (!c.attempted.IsZero() && time.Since(c.attempted) < modelsCatalogRetry) {
		done := make(chan struct{})
		close(done)
		return done
	}
	c.attempted = time.Now()
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(parent, modelsCatalogTimeout)
	c.refreshing, c.refreshCancel = done, cancel
	go func() {
		defer cancel()
		providers, err := c.fetch(ctx)
		updated := time.Now()
		c.mu.Lock()
		publish := err == nil && ctx.Err() == nil && c.refreshing == done
		if publish {
			c.providers, c.updated = providers, updated
		}
		c.mu.Unlock()
		if publish {
			c.persist(providers, updated)
		}
		c.mu.Lock()
		if c.refreshing == done {
			c.refreshing, c.refreshCancel = nil, nil
		}
		close(done)
		c.mu.Unlock()
	}()
	return done
}

func (c *ModelCatalog) fetch(ctx context.Context) (map[string]catalogProvider, error) {
	// Do not reuse the authenticated provider client or follow redirects.
	client := &http.Client{Transport: netproxy.Transport(nil), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > modelsCatalogMaxBytes {
		return nil, errors.New("models.dev catalog unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, modelsCatalogMaxBytes+1))
	if err != nil || len(data) > modelsCatalogMaxBytes {
		return nil, errors.New("models.dev catalog unreadable or too large")
	}
	var providers map[string]catalogProvider
	if json.Unmarshal(data, &providers) != nil || !usableCatalog(providers) {
		return nil, errors.New("models.dev catalog malformed")
	}
	return providers, nil
}

func validCatalogModel(id string, entry catalogModel) bool {
	return id != "" && entry.ID == id && entry.Limit.Context >= 4096 && entry.Limit.Context <= 2097152 && entry.Limit.Input >= 0 && entry.Limit.Input <= 2097152
}

func usableCatalog(providers map[string]catalogProvider) bool {
	for _, provider := range providers {
		for id, entry := range provider.Models {
			if validCatalogModel(id, entry) {
				return true
			}
		}
	}
	return false
}

func (c *ModelCatalog) persist(providers map[string]catalogProvider, updated time.Time) {
	if c.path == "" {
		return
	}
	data, err := json.Marshal(catalogFile{Version: 1, UpdatedAt: updated, Providers: providers})
	if err != nil || len(data) > modelsCatalogMaxBytes || os.MkdirAll(filepath.Dir(c.path), 0o700) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".models-dev-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		_ = os.Rename(f.Name(), c.path)
	}
}

func catalogServiceKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	path = strings.TrimSuffix(path, "/chat/completions")
	return strings.ToLower(u.Scheme+"://"+u.Host) + path
}

func (c *ModelCatalog) lookup(baseURL, model, providerID, modelID string) (catalogMatch, bool) {
	if c == nil {
		return catalogMatch{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if modelID == "" {
		modelID = model
	}
	match := func(id string, provider catalogProvider) (catalogMatch, bool) {
		entry, ok := provider.Models[modelID]
		if !ok || !validCatalogModel(modelID, entry) {
			return catalogMatch{}, false
		}
		return catalogMatch{window: entry.Limit.Context, input: entry.Limit.Input, reference: id + "/" + modelID}, true
	}
	if providerID != "" {
		return match(providerID, c.providers[providerID])
	}
	// A model override without an explicit provider is never a global name search.
	if modelID != model {
		return catalogMatch{}, false
	}
	key := catalogServiceKey(baseURL)
	if key == "" {
		return catalogMatch{}, false
	}
	var found catalogMatch
	count := 0
	for id, provider := range c.providers {
		api := provider.API
		// Native SDK providers can omit api. Only known exact endpoints qualify.
		if api == "" && id == "openai" {
			api = "https://api.openai.com/v1"
		}
		if catalogServiceKey(api) == key {
			if value, ok := match(id, provider); ok {
				found = value
				count++
			}
		}
	}
	return found, count == 1
}
