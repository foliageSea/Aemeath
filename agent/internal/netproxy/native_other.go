//go:build !darwin && !windows

package netproxy

import "context"

func readNative(ctx context.Context) (nativeConfig, error) { return nativeConfig{}, ctx.Err() }
