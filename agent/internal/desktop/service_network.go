package desktop

import (
	"context"

	"aemeath/agent/internal/netproxy"
)

// NetworkContext attaches the saved routing policy to an independent operation,
// such as a model installation while the device/voice session is stopped. The
// caller retains ownership of cancellation. Closing the service closes its Router.
func (s *Service) NetworkContext(ctx context.Context) context.Context {
	return netproxy.WithRouter(ctx, s.network)
}
