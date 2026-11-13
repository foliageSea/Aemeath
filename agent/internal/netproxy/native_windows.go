//go:build windows

package netproxy

import (
	"context"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	getIEProxyConfig = windows.NewLazySystemDLL("winhttp.dll").NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	globalFree       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
	// The native call reads current-user WinINET settings, not PAC scripts or the
	// network. Since Windows offers no cancellation API, bound callers' waits and
	// allow at most one outstanding native call even if the OS becomes unresponsive.
	nativeReadGate = make(chan struct{}, 1)
)

type ieProxyConfig struct {
	AutoDetect                        int32 // Windows BOOL, followed by pointer-aligned LPWSTRs.
	AutoConfigURL, Proxy, ProxyBypass *uint16
}

type nativeResult struct {
	config nativeConfig
	err    error
}

func readNative(ctx context.Context) (nativeConfig, error) {
	select {
	case nativeReadGate <- struct{}{}:
	case <-ctx.Done():
		return nativeConfig{}, ctx.Err()
	}
	done := make(chan nativeResult, 1)
	go func() {
		defer func() { <-nativeReadGate }()
		n, err := readCurrentUserProxy()
		done <- nativeResult{n, err}
	}()
	select {
	case result := <-done:
		return result.config, result.err
	case <-ctx.Done():
		return nativeConfig{}, ctx.Err()
	}
}

func readCurrentUserProxy() (nativeConfig, error) {
	if err := getIEProxyConfig.Find(); err != nil {
		return nativeConfig{}, errNativeSettings
	}
	var config ieProxyConfig
	ok, _, callErr := getIEProxyConfig.Call(uintptr(unsafe.Pointer(&config)))
	// WinHTTP allocates these strings with GlobalAlloc; free even on partial failure.
	defer func() {
		for _, p := range []*uint16{config.AutoConfigURL, config.Proxy, config.ProxyBypass} {
			if p != nil {
				globalFree.Call(uintptr(unsafe.Pointer(p)))
			}
		}
	}()
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) {
			return nativeConfig{}, nil
		}
		return nativeConfig{}, errNativeSettings
	}
	server := windows.UTF16PtrToString(config.Proxy)
	bypass := windows.UTF16PtrToString(config.ProxyBypass)
	if len(server)+len(bypass) > 64*1024 {
		return nativeConfig{}, errNativeSettings
	}
	// Unlike reading the registry URL alone, this API returns the active PAC/WPAD
	// flags for the current user and does not mistake stale disabled settings for PAC.
	auto := config.AutoDetect != 0 || config.AutoConfigURL != nil && windows.UTF16PtrToString(config.AutoConfigURL) != ""
	return parseWindowsProxy(server, bypass, server != "", auto)
}
