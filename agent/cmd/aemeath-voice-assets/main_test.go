package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"aemeath/agent/internal/netproxy"
	"aemeath/agent/internal/wake"
)

func TestVoiceAssetProxyArgs(t *testing.T) {
	opts, err := parseArgs(nil, io.Discard)
	if err != nil || opts.network.Mode != netproxy.ModeSystem || opts.dir != wake.DefaultAssetDir() || opts.verify || opts.reference {
		t.Fatalf("default options=%+v, err=%v", opts, err)
	}
	for _, mode := range []string{netproxy.ModeSystem, netproxy.ModeDirect, netproxy.ModeProxy} {
		for _, scheme := range []string{"http", "https", "socks5"} {
			url := scheme + "://user:secret@127.0.0.1:7890"
			opts, err := parseArgs([]string{"--proxy-mode=" + mode, "--proxy-url=" + url, "--verify", "--reference-only", "--dir=custom"}, io.Discard)
			if err != nil || opts.network != (netproxy.Config{Mode: mode, ProxyURL: url}) || !opts.verify || !opts.reference || opts.dir != "custom" {
				t.Fatalf("proxy/asset options=%+v, err=%v", opts, err)
			}
		}
	}
	opts, err = parseArgs([]string{"--proxy-mode= DIRECT ", "--proxy-url=unfinished"}, io.Discard)
	if err != nil || opts.network.Mode != netproxy.ModeDirect || opts.network.ProxyURL != "unfinished" {
		t.Fatalf("inactive URL should not require validation: %+v, %v", opts, err)
	}
}

func TestVoiceAssetInvalidArgsAndHelp(t *testing.T) {
	for _, args := range [][]string{
		{"--proxy-mode=auto"}, {"--proxy-mode=proxy"},
		{"--proxy-mode=proxy", "--proxy-url=ftp://user:private-password@localhost:1234"},
		{"unexpected"},
	} {
		var output, errorOutput bytes.Buffer
		if code := run(args, &output, &errorOutput); code != 2 || output.Len() != 0 || errorOutput.Len() == 0 || strings.Contains(errorOutput.String(), "private-password") {
			t.Fatalf("invalid args exit/output=%d/%q/%q", code, output.String(), errorOutput.String())
		}
	}
	var help bytes.Buffer
	if code := run([]string{"--help"}, io.Discard, &help); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
	for _, text := range []string{"proxy-mode", "proxy-url", "system", "socks5", "direct"} {
		if !strings.Contains(help.String(), text) {
			t.Fatalf("help missing %q", text)
		}
	}
}

func TestVoiceAssetVerifyStaysOffline(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected network", http.StatusBadGateway)
	}))
	defer proxy.Close()
	dir := t.TempDir()
	var output, errorOutput bytes.Buffer
	code := run([]string{"--verify", "--dir=" + dir, "--proxy-mode=proxy", "--proxy-url=" + proxy.URL}, &output, &errorOutput)
	if code != 1 || calls.Load() != 0 || output.Len() != 0 || errorOutput.Len() == 0 {
		t.Fatalf("verify exit=%d proxy calls=%d output=%q error=%q", code, calls.Load(), output.String(), errorOutput.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("verification created files", err)
	}
}

func TestVoiceAssetInstallUsesProxyContext(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "proxy rejects download", http.StatusBadGateway)
	}))
	defer proxy.Close()
	var output, errorOutput bytes.Buffer
	code := run([]string{"--reference-only", "--dir=" + t.TempDir(), "--proxy-mode=proxy", "--proxy-url=" + proxy.URL}, &output, &errorOutput)
	if code != 1 || calls.Load() != 1 || output.Len() != 0 || errorOutput.Len() == 0 {
		t.Fatalf("install did not use context proxy: exit=%d calls=%d output=%q error=%q", code, calls.Load(), output.String(), errorOutput.String())
	}
}
