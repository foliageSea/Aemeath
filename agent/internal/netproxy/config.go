// Package netproxy provides request-scoped network routing without changing
// process environment variables or the net/http default transport.
package netproxy

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	ModeSystem = "system"
	ModeProxy  = "proxy"
	ModeDirect = "direct"
)

type Config struct {
	Mode     string `json:"mode"`
	ProxyURL string `json:"proxyURL"`
}

// Normalize defaults the mode and validates only active settings. Inactive
// ProxyURL values are deliberately preserved, including temporarily invalid ones.
func Normalize(c Config) (Config, error) {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = ModeSystem
	}
	switch c.Mode {
	case ModeSystem, ModeDirect:
	case ModeProxy:
		if _, err := parseProxy(c.ProxyURL, true); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, errors.New("netproxy: invalid network mode")
	}
	return c, nil
}

// parseProxy never returns the original URL in an error: it may contain secrets.
func parseProxy(raw string, explicitPort bool) (*url.URL, error) {
	invalid := errors.New("netproxy: invalid proxy URL (expected http, https or socks5 host and port without a path, query or fragment)")
	if raw == "" || strings.ContainsAny(raw, "?#") || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return nil, invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, invalid
	}
	if u.User != nil {
		password, _ := u.User.Password()
		if strings.IndexFunc(u.User.Username()+password, unicode.IsControl) >= 0 {
			return nil, invalid
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" {
		return nil, invalid
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "\\/%") {
		return nil, invalid
	}
	// Brackets are reserved for valid IPv6 literals; unbracketed colons are invalid.
	if strings.Contains(host, ":") {
		if ip, err := netip.ParseAddr(host); err != nil || !ip.Is6() || !strings.HasPrefix(u.Host, "[") {
			return nil, invalid
		}
	} else if strings.ContainsAny(host, "[]") {
		return nil, invalid
	}
	port := u.Port()
	if port == "" {
		if explicitPort || strings.HasSuffix(u.Host, ":") {
			return nil, invalid
		}
	} else {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 || strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return nil, invalid
		}
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			return nil, invalid
		}
	}
	return u, nil
}

// localHost uses literals and hostname syntax only; it must never resolve DNS.
func localHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "local" || strings.HasSuffix(host, ".local") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
	}
	return false
}
