package netproxy

import (
	"net"
	"strconv"
	"strings"
)

// parseSCUtil handles scutil's dictionary format, including the indexed
// ExceptionsList array. Only enabled manual settings are interpreted.
func parseSCUtil(output string) (nativeConfig, error) {
	n := nativeConfig{}
	values := make(map[string]string)
	depth, exceptionsDepth := 0, 0
	started := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if depth == 0 {
			// Require exactly one complete root, not merely a dictionary marker.
			if started || line != "<dictionary> {" {
				return nativeConfig{}, errNativeSettings
			}
			started, depth = true, 1
			continue
		}
		if line == "}" {
			if depth == exceptionsDepth {
				exceptionsDepth = 0
			}
			depth--
			if depth < 0 {
				return nativeConfig{}, errNativeSettings
			}
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		opens := strings.HasSuffix(line, "{") && (strings.Contains(line, "<dictionary>") || strings.Contains(line, "<array>"))
		if depth == 1 && ok {
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if key == "ExceptionsList" && strings.HasPrefix(value, "<array>") {
				exceptionsDepth = depth + 1
			} else if !opens {
				values[key] = value
			}
		} else if exceptionsDepth > 0 && depth == exceptionsDepth && ok && !opens {
			n.Bypass = append(n.Bypass, strings.TrimSpace(value))
		}
		if opens {
			depth++
		}
	}
	if !started || depth != 0 {
		return nativeConfig{}, errNativeSettings
	}
	enabled := func(key string) bool { return values[key] == "1" }
	if enabled("ExcludeSimpleHostnames") {
		n.Bypass = append(n.Bypass, "<local>")
	}
	n.Auto = enabled("ProxyAutoConfigEnable") || enabled("ProxyAutoDiscoveryEnable")
	for _, item := range []struct {
		key, scheme string
		target      *string
	}{{"HTTP", "http", &n.HTTP}, {"HTTPS", "http", &n.HTTPS}, {"SOCKS", "socks5", &n.SOCKS}} {
		if !enabled(item.key + "Enable") {
			continue
		}
		host, port := values[item.key+"Proxy"], values[item.key+"Port"]
		if host == "" || strings.ContainsAny(host, "/@?#") {
			return nativeConfig{}, errNativeSettings
		}
		if _, err := strconv.Atoi(port); err != nil {
			return nativeConfig{}, errNativeSettings
		}
		raw := item.scheme + "://" + net.JoinHostPort(strings.Trim(host, "[]"), port)
		if _, err := parseProxy(raw, true); err != nil {
			return nativeConfig{}, errNativeSettings
		}
		*item.target = raw
	}
	return n, nil
}

// parseWindowsProxy accepts WinINET's global host:port and protocol-keyed lists.
// HTTPS keys describe the destination scheme, not TLS to the proxy itself.
func parseWindowsProxy(server, bypass string, enabled, auto bool) (nativeConfig, error) {
	n := nativeConfig{Auto: auto, Bypass: strings.FieldsFunc(bypass, func(r rune) bool { return r == ';' || r == '\n' })}
	if !enabled {
		return n, nil
	}
	if strings.TrimSpace(server) == "" {
		return nativeConfig{}, errNativeSettings
	}
	for _, part := range strings.Split(server, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, endpoint, keyed := strings.Cut(part, "=")
		if !keyed {
			endpoint, key = key, ""
		}
		key, endpoint = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(endpoint)
		if key != "" && key != "http" && key != "https" && key != "socks" && key != "socks5" {
			continue
		}
		scheme := "http"
		if key == "socks" || key == "socks5" {
			scheme = "socks5"
		}
		if !strings.Contains(endpoint, "://") {
			endpoint = scheme + "://" + endpoint
		}
		if _, err := parseProxy(endpoint, true); err != nil {
			return nativeConfig{}, errNativeSettings
		}
		switch key {
		case "http":
			n.HTTP = endpoint
		case "https":
			n.HTTPS = endpoint
		case "socks", "socks5":
			n.SOCKS = endpoint
		default:
			n.HTTP, n.HTTPS = endpoint, endpoint
		}
	}
	if n.HTTP == "" && n.HTTPS == "" && n.SOCKS == "" && !n.Auto {
		return nativeConfig{}, errNativeSettings
	}
	return n, nil
}
