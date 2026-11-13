package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

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
