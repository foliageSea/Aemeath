// Package desktop provides the background agent used by desktop UIs.
package desktop

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"aemeath/agent/internal/magpie"
	"aemeath/agent/internal/netproxy"
	"aemeath/agent/internal/voice"
)

type Subscription struct {
	Name   string `json:"name"`
	Hidden bool   `json:"hidden"`
}

const (
	RunModeDevice     = "device"
	RunModeStandalone = "standalone"
)

type Settings struct {
	RunMode       string          `json:"runMode"`
	Address       string          `json:"address"`
	MagpieURL     string          `json:"magpieURL"`
	Subscriptions []Subscription  `json:"subscriptions"`
	Voice         voice.Config    `json:"voice"`
	Network       netproxy.Config `json:"network"`
}

func normalizeSettings(in Settings) (Settings, error) {
	in.RunMode = strings.ToLower(strings.TrimSpace(in.RunMode))
	switch in.RunMode {
	case "":
		in.RunMode = RunModeDevice
	case RunModeDevice, RunModeStandalone:
	default:
		return Settings{}, fmt.Errorf("invalid run mode %q: expected device or standalone", in.RunMode)
	}
	var err error
	in.Network, err = netproxy.Normalize(in.Network)
	if err != nil {
		return Settings{}, fmt.Errorf("network settings: %w", err)
	}
	in.Voice, err = voice.NormalizeConfig(in.Voice)
	if err != nil {
		return Settings{}, fmt.Errorf("voice settings: %w", err)
	}
	in.Address = strings.TrimSpace(in.Address)
	if err := validateAddress(in.Address); err != nil {
		return Settings{}, err
	}
	in.MagpieURL = strings.TrimRight(strings.TrimSpace(in.MagpieURL), "/")
	if in.MagpieURL == "" {
		in.MagpieURL = magpie.DefaultBase
	}
	u, err := url.Parse(in.MagpieURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Settings{}, fmt.Errorf("invalid Magpie URL: %q", in.MagpieURL)
	}
	in.Subscriptions = append([]Subscription{}, in.Subscriptions...)
	seen := map[string]bool{}
	for _, sub := range in.Subscriptions {
		if sub.Name == "" || seen[sub.Name] {
			return Settings{}, fmt.Errorf("subscription names must be nonempty and unique: %q", sub.Name)
		}
		seen[sub.Name] = true
	}
	return in, nil
}

func validateAddress(address string) error {
	if address == "" {
		return nil
	}
	invalid := func() error {
		return fmt.Errorf("invalid device address %q: expected hostname/IP or host:port", address)
	}
	if strings.ContainsAny(address, "/@?#") || strings.ContainsFunc(address, unicode.IsSpace) {
		return invalid()
	}
	if net.ParseIP(strings.Trim(address, "[]")) != nil {
		return nil
	}
	host := address
	if strings.ContainsAny(address, ":[]") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(address)
		if err != nil {
			return invalid()
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return invalid()
		}
		if net.ParseIP(host) != nil {
			return nil
		}
	}
	if len(host) == 0 || len(host) > 253 {
		return invalid()
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return invalid()
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return invalid()
			}
		}
	}
	return nil
}

func loadSettings(path string) (Settings, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return normalizeSettings(Settings{Voice: voice.Config{Volume: 70}})
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	// Missing voice/volume fields migrate to an audible default; explicit zero stays muted.
	settings := Settings{Voice: voice.Config{Volume: 70}}
	if err := json.Unmarshal(b, &settings); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	return normalizeSettings(settings)
}

func persistSettings(path string, settings Settings) error {
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".desktop-settings-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(append(b, byte(10))); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return nil
}
