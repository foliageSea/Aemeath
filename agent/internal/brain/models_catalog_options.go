package brain

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// ModelCatalogProvider is a public, context-capable model selection. It contains
// only catalog IDs, never API endpoints or configured credentials.
type ModelCatalogProvider struct {
	ID     string   `json:"id"`
	Models []string `json:"models"`
}

var errModelCatalogUnavailable = errors.New("models.dev catalog unavailable")

// Options returns a sorted, independently owned snapshot of usable catalog IDs.
// Cached metadata remains usable while a stale catalog refreshes in the background.
// Without cached metadata, Options waits for the shared refresh or ctx cancellation.
// Download errors are deliberately not exposed, since they may contain proxy details.
func (c *ModelCatalog) Options(ctx context.Context) ([]ModelCatalogProvider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errModelCatalogUnavailable
	}
	options := c.optionsSnapshot()
	done := c.RefreshAsync(ctx)
	if len(options) != 0 {
		return options, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options = c.optionsSnapshot(); len(options) != 0 {
		return options, nil
	}
	return nil, errModelCatalogUnavailable
}

func (c *ModelCatalog) optionsSnapshot() []ModelCatalogProvider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	options := make([]ModelCatalogProvider, 0, len(c.providers))
	for id, provider := range c.providers {
		if strings.TrimSpace(id) == "" {
			continue
		}
		models := make([]string, 0, len(provider.Models))
		for modelID, model := range provider.Models {
			if validCatalogModel(modelID, model) {
				models = append(models, modelID)
			}
		}
		if len(models) != 0 {
			sort.Strings(models)
			options = append(options, ModelCatalogProvider{ID: id, Models: models})
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options
}
