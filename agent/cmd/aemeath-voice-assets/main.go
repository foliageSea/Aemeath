// aemeath-voice-assets explicitly installs pinned offline voice assets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"aemeath/agent/internal/netproxy"
	"aemeath/agent/internal/wake"
)

type cliOptions struct {
	dir       string
	reference bool
	verify    bool
	network   netproxy.Config
}

func parseArgs(args []string, output io.Writer) (cliOptions, error) {
	var opts cliOptions
	flags := flag.NewFlagSet("aemeath-voice-assets", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.dir, "dir", wake.DefaultAssetDir(), "voice asset cache directory")
	flags.BoolVar(&opts.reference, "reference-only", false, "install or verify only the reference WAV")
	flags.BoolVar(&opts.verify, "verify", false, "verify local assets without downloading")
	flags.StringVar(&opts.network.Mode, "proxy-mode", netproxy.ModeSystem, "network proxy mode: system, proxy, or direct; direct ignores environment and system proxies")
	flags.StringVar(&opts.network.ProxyURL, "proxy-url", "", "manual proxy URL for proxy mode (http, https, socks5; failure never falls back to direct)")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument: %s", flags.Arg(0))
	}
	network, err := netproxy.Normalize(opts.network)
	if err != nil {
		return opts, fmt.Errorf("proxy configuration: %w", err)
	}
	opts.network = network
	return opts, nil
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, output, errorOutput io.Writer) int {
	opts, err := parseArgs(args, errorOutput)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(errorOutput, "voice assets:", err)
		return 2
	}
	router, err := netproxy.New(opts.network)
	if err != nil {
		fmt.Fprintln(errorOutput, "voice assets:", err)
		return 1
	}
	defer router.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx = netproxy.WithRouter(ctx, router)
	if opts.verify {
		err = wake.VerifyAssets(opts.dir, opts.reference)
	} else {
		err = wake.InstallAssets(ctx, opts.dir, opts.reference)
	}
	if err != nil {
		fmt.Fprintln(errorOutput, "voice assets:", err)
		return 1
	}
	fmt.Fprintf(output, "Voice assets verified: %s\nChinese wake: %s; sherpa-onnx %s; Silero VAD\n", opts.dir, wake.Keyword, wake.BindingVersion)
	return 0
}
