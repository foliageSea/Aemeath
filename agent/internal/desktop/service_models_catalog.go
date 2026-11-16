package desktop

import (
	"context"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/netproxy"
)

// ModelCatalogOptions lists public models.dev IDs using this service's catalog
// and saved network policy, independently of device/voice connectivity.
// No configured provider address, model alias, or credential is sent.
func (s *Service) ModelCatalogOptions(ctx context.Context) ([]brain.ModelCatalogProvider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return (*brain.ModelCatalog)(nil).Options(ctx)
	}
	s.mu.RLock()
	catalog, network, serviceCtx, closed := s.catalog, s.network, s.ctx, s.closed
	s.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if catalog != nil {
		if serviceCtx == nil {
			serviceCtx = context.Background()
		}
		// The service owns the shared download. A UI waiter may cancel or
		// return a stale snapshot without aborting work for other callers.
		catalog.RefreshAsync(netproxy.WithRouter(serviceCtx, network))
	}
	return catalog.Options(netproxy.WithRouter(ctx, network))
}
