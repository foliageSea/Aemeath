package voice

import "context"

// RunStandalone opens the system default microphone and runs one local voice
// session without a device connection. The caller owns retries. Cloud services
// and wake assets use the same configuration as Run. The effective speaker route
// is always the local computer; the caller's saved playback choice is preserved.
func RunStandalone(ctx context.Context, cfg Config, host Host, commands <-chan Command, emit func(State)) error {
	return runStandaloneWithDependencies(ctx, cfg, host, commands, emit, productionDependencies())
}

func runStandaloneWithDependencies(ctx context.Context, cfg Config, host Host, commands <-chan Command, emit func(State), d dependencies) error {
	return runSession(ctx, "", cfg, host, commands, emit, d, true)
}
