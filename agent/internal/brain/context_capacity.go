package brain

import (
	"context"
	"strings"
)

// Public configuration identity prevents a previous connection's statistics
// from masking changed settings while its replacement Engine is still lazy.
// Credentials are deliberately not retained here.
type contextConfigKey struct {
	baseURL, model, persona       string
	window                        int
	auto, tools                   bool
	catalog                       *ModelCatalog
	catalogProvider, catalogModel string
	longTerm                      *LongTermMemory
	memoryMode                    string
}

func contextConfigFor(cfg Config) contextConfigKey {
	base := cfg.BaseURL
	if strings.TrimSpace(base) == "" {
		base = "https://api.openai.com/v1"
	}
	persona := strings.TrimSpace(cfg.Persona)
	if persona == "" {
		persona = DefaultPersona
	}
	window, _ := NormalizeContextWindow(cfg.ContextWindow)
	key := contextConfigKey{baseURL: catalogServiceKey(base), model: strings.TrimSpace(cfg.Model), persona: persona, window: window, auto: cfg.AutoContextWindow, tools: cfg.Tools}
	key.longTerm = cfg.LongTermMemory
	key.memoryMode, _ = NormalizeMemoryMode(cfg.MemoryMode)
	if cfg.AutoContextWindow {
		key.catalog, key.catalogProvider, key.catalogModel = cfg.ModelsDev, cfg.ModelsDevProvider, cfg.ModelsDevModel
	}
	return key
}

// Refresh capacity from an already loaded catalog between turns. The configured
// conversation service is never queried for model metadata, and downloads do
// not block a reply or change an in-flight request's budget.
func (e *Engine) resolveContextWindow(ctx context.Context, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.requestCurrentLocked(requestScope{generation: generation, epoch: e.memoryEpoch}) {
		return context.Canceled
	}
	config := e.contextConfig
	if !config.auto {
		return nil
	}
	if config.catalog != nil {
		config.catalog.RefreshAsync(e.lifeCtx)
	}
	capacity := contextCapacityStats(config)
	applyContextCapacity(&e.stats, capacity.ContextWindow, capacity.ContextInputLimit, capacity.ContextWindowSource, capacity.ContextWindowReference)
	e.stats.ContextWindowModel = capacity.ContextWindowModel
	e.updateContextLocked()
	return nil
}

func contextCapacityStats(config contextConfigKey) ContextStats {
	s := newContextStats(config.window)
	s.ContextWindowModel, s.ContextWindowSource = config.model, "manual"
	if config.auto {
		s.ContextWindowSource = "fallback"
		if match, ok := config.catalog.lookup(config.baseURL, config.model, config.catalogProvider, config.catalogModel); ok {
			applyContextCapacity(&s, match.window, match.input, "modelsdev", match.reference)
		}
	}
	return s
}

func applyContextCapacity(stats *ContextStats, window, input int, source, reference string) {
	budget := newContextStats(window)
	if input > 0 {
		budget.InputBudget = min(budget.InputBudget, input)
		budget.ReservedTokens = window - budget.InputBudget
	}
	stats.ContextWindow, stats.InputBudget, stats.ReservedTokens = window, budget.InputBudget, budget.ReservedTokens
	stats.ContextInputLimit, stats.ContextWindowSource, stats.ContextWindowReference = input, source, reference
}
