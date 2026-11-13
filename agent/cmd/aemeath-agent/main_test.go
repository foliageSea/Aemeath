package main

import (
	"bytes"
	"errors"
	"flag"

	"aemeath/agent/internal/netproxy"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCLIProxyArgsNormalizeModesAndPreserveInactiveURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want netproxy.Config
	}{
		{"default", nil, netproxy.Config{Mode: netproxy.ModeSystem}},
		{"legacy-empty", []string{"--proxy-mode="}, netproxy.Config{Mode: netproxy.ModeSystem}},
		{"system", []string{"--proxy-mode=system"}, netproxy.Config{Mode: netproxy.ModeSystem}},
		{"direct", []string{"--proxy-mode= DIRECT ", "--proxy-url=unfinished draft"}, netproxy.Config{Mode: netproxy.ModeDirect, ProxyURL: "unfinished draft"}},
		{"inactive-system", []string{"--proxy-url=unfinished draft"}, netproxy.Config{Mode: netproxy.ModeSystem, ProxyURL: "unfinished draft"}},
		{"http", []string{"--proxy-mode=proxy", "--proxy-url=http://127.0.0.1:7890"}, netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "http://127.0.0.1:7890"}},
		{"https", []string{"--proxy-mode=proxy", "--proxy-url=https://user:secret@proxy.example:8443"}, netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "https://user:secret@proxy.example:8443"}},
		{"socks5", []string{"--standalone", "--proxy-mode=proxy", "--proxy-url=socks5://127.0.0.1:1080"}, netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: "socks5://127.0.0.1:1080"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseArgs(tc.args, io.Discard)
			if err != nil || opts.network != tc.want {
				t.Fatalf("network=%+v, err=%v; want %+v", opts.network, err, tc.want)
			}
		})
	}
}

func TestCLIProxyArgsRejectInvalidConfigurationWithoutSecrets(t *testing.T) {
	for _, args := range [][]string{
		{"--proxy-mode=auto"},
		{"--proxy-mode=proxy"},
		{"--proxy-mode=proxy", "--proxy-url=socks4://127.0.0.1:1080"},
		{"--proxy-mode=proxy", "--proxy-url=http://user:private-password@proxy.example:7890/path"},
	} {
		_, err := parseArgs(args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "proxy") || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("invalid proxy args accepted or exposed secrets: err=%v", err)
		}
	}
}

func TestCLIProxyHelp(t *testing.T) {
	var output bytes.Buffer
	_, err := parseArgs([]string{"--help"}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error=%v", err)
	}
	for _, text := range []string{"proxy-mode", "proxy-url", "system", "socks5", "direct"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("proxy help missing %q", text)
		}
	}
}

func TestStandaloneArgsEnableVoiceWithoutVoiceFlag(t *testing.T) {
	for _, args := range [][]string{{"--standalone"}, {"-standalone", "--voice=false"}, {"--standalone", "--interval=250ms", "--magpie="}} {
		opts, err := parseArgs(args, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if !opts.standalone || !opts.voiceEnabled {
			t.Fatalf("standalone must enable voice: %+v", opts)
		}
		cfg, err := loadVoiceConfig(opts.voiceConfig, opts.voiceEnabled)
		if err != nil || !cfg.Enabled {
			t.Fatalf("standalone voice config enabled=%t, err=%v", cfg.Enabled, err)
		}
	}
}

func TestStandaloneArgsRejectConflicts(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--standalone", "--addr=example.test:80"}, "--addr"},
		{[]string{"--addr", "example.test:80", "--standalone"}, "--addr"},
		{[]string{"--standalone", "--addr="}, "--addr"},
		{[]string{"--standalone", "--dry-run"}, "--dry-run"},
		{[]string{"--dry-run", "--standalone"}, "--dry-run"},
		{[]string{"--standalone", "--interval=0"}, "interval must be positive"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, err := parseArgs(tc.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseArgs(%q)=%v, want %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestCLIArgsPreserveDeviceAndDryRunModes(t *testing.T) {
	opts, err := parseArgs(nil, io.Discard)
	if err != nil || opts.standalone || opts.voiceEnabled || opts.interval != time.Second {
		t.Fatalf("default options=%+v, err=%v", opts, err)
	}
	opts, err = parseArgs([]string{"--addr=example.test:80", "--dry-run", "--voice-config=private.json", "--v"}, io.Discard)
	if err != nil || opts.standalone || opts.addr != "example.test:80" || !opts.dryRun || !opts.verbose || opts.voiceConfig != "private.json" {
		t.Fatalf("device/dry-run options=%+v, err=%v", opts, err)
	}
}
