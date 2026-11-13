// aemeath-agent streams PC metrics to ESP32 or runs a standalone voice assistant.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"time"

	"aemeath/agent/internal/brain"
	"aemeath/agent/internal/collect"
	"aemeath/agent/internal/discover"
	"aemeath/agent/internal/link"
	"aemeath/agent/internal/magpie"
)

const quotaInterval = 30 * time.Second

type cliOptions struct {
	addr         string
	magpieURL    string
	voiceConfig  string
	interval     time.Duration
	verbose      bool
	dryRun       bool
	voiceEnabled bool
	standalone   bool
}

func parseArgs(args []string, output io.Writer) (cliOptions, error) {
	var opts cliOptions
	flags := flag.NewFlagSet("aemeath-agent", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.addr, "addr", "", "device host:port (skips mDNS discovery)")
	flags.DurationVar(&opts.interval, "interval", time.Second, "sample interval")
	flags.BoolVar(&opts.verbose, "v", false, "log every frame")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "print frames to stdout instead of sending")
	flags.StringVar(&opts.magpieURL, "magpie", magpie.DefaultBase, "Magpie gateway base URL for quota frames (empty disables)")
	flags.BoolVar(&opts.voiceEnabled, "voice", false, "enable the voice assistant using environment configuration")
	flags.StringVar(&opts.voiceConfig, "voice-config", "", "voice configuration JSON file (enables voice)")
	flags.BoolVar(&opts.standalone, "standalone", false, "use system default microphone and speaker without an ESP32 (enables voice)")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument: %s", flags.Arg(0))
	}
	if opts.interval <= 0 {
		return opts, errors.New("interval must be positive")
	}
	var addrSet bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			addrSet = true
		}
	})
	if opts.standalone && addrSet {
		return opts, errors.New("--standalone cannot be combined with --addr")
	}
	if opts.standalone && opts.dryRun {
		return opts, errors.New("--standalone cannot be combined with --dry-run")
	}
	opts.voiceEnabled = opts.voiceEnabled || opts.standalone
	return opts, nil
}

func main() {
	opts, err := parseArgs(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	vcfg, err := loadVoiceConfig(opts.voiceConfig, opts.voiceEnabled)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c := collect.New()
	defer c.Close()
	var mp *magpie.Client
	if opts.magpieURL != "" {
		mp = magpie.New(opts.magpieURL)
	}

	if opts.dryRun {
		runDry(ctx, c, mp, opts.interval)
		return
	}

	control := &cliVoiceControl{}
	if vcfg.Enabled {
		log.Print(cliVoiceHelp)
		go control.console(ctx, os.Stdin)
	}
	backoff := time.Second
	// CLI keeps the conversation across reconnects, in process memory only.
	memory := brain.OpenMemory("")
	if opts.standalone {
		log.Print("standalone: system default microphone and speaker; playback output: console (system speaker; effective only); no ESP32 connection")
		attempt, cancel := context.WithCancel(ctx)
		host := &cliVoiceHost{memory: memory}
		voiceDone := control.startStandalone(attempt, vcfg, host)
		_ = stream(attempt, c, mp, nil, opts.interval, opts.verbose, host)
		cancel()
		<-voiceDone
		return
	}
	for ctx.Err() == nil {
		target := opts.addr
		if target == "" {
			found, err := discover.Find(ctx, 5*time.Second)
			if err != nil {
				log.Printf("discover: %v (retry in %s)", err, backoff)
				sleep(ctx, &backoff)
				continue
			}
			target = found
		}
		conn, err := link.Dial(ctx, target)
		if err != nil {
			log.Printf("connect %s: %v (retry in %s)", target, err, backoff)
			sleep(ctx, &backoff)
			continue
		}
		log.Printf("connected to %s", target)
		backoff = time.Second
		attempt, cancel := context.WithCancel(ctx)
		host := &cliVoiceHost{conn: conn, memory: memory}
		voiceDone := control.start(attempt, target, vcfg, host)
		err = stream(attempt, c, mp, conn, opts.interval, opts.verbose, host)
		cancel()
		<-voiceDone
		conn.Close()
		if ctx.Err() == nil {
			log.Printf("connection lost: %v", err)
		}
	}
}

func stream(ctx context.Context, c *collect.Collector, mp *magpie.Client, conn *link.Conn, interval time.Duration, verbose bool, host *cliVoiceHost) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastQuota time.Time
	for {
		if mp != nil && time.Since(lastQuota) >= quotaInterval {
			lastQuota = time.Now()
			// Magpie being down must not drop the metrics link.
			if qf, err := mp.Frame(ctx); err != nil {
				log.Printf("magpie: %v", err)
			} else {
				host.setQuota(qf)
				if conn != nil {
					if err := conn.Send(ctx, qf); err != nil {
						return err
					}
				}
				if verbose {
					log.Printf("quota frame: %d accounts", len(qf.Accounts))
				}
			}
		}
		f := c.Sample(ctx)
		host.setMetrics(f)
		if conn != nil {
			if err := conn.Send(ctx, f); err != nil {
				return err
			}
		}
		if verbose {
			b, _ := json.Marshal(f)
			log.Printf("metrics: %s", b)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func runDry(ctx context.Context, c *collect.Collector, mp *magpie.Client, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastQuota time.Time
	for {
		if mp != nil && time.Since(lastQuota) >= quotaInterval {
			lastQuota = time.Now()
			if qf, err := mp.Frame(ctx); err != nil {
				log.Printf("magpie: %v", err)
			} else {
				b, _ := json.Marshal(qf)
				fmt.Println(string(b))
			}
		}
		b, _ := json.Marshal(c.Sample(ctx))
		fmt.Println(string(b))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func sleep(ctx context.Context, backoff *time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(*backoff):
	}
	if *backoff *= 2; *backoff > 30*time.Second {
		*backoff = 30 * time.Second
	}
}
