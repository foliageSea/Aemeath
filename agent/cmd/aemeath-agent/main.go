// aemeath-agent streams PC metrics to the Aemeath ESP32 dashboard.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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

func main() {
	addr := flag.String("addr", "", "device host:port (skips mDNS discovery)")
	interval := flag.Duration("interval", time.Second, "sample interval")
	verbose := flag.Bool("v", false, "log every frame")
	dryRun := flag.Bool("dry-run", false, "print frames to stdout instead of sending")
	magpieURL := flag.String("magpie", magpie.DefaultBase, "Magpie gateway base URL for quota frames (empty disables)")
	voiceEnabled := flag.Bool("voice", false, "enable the voice assistant using environment configuration")
	voiceConfig := flag.String("voice-config", "", "voice configuration JSON file (enables voice)")
	flag.Parse()
	if *interval <= 0 {
		log.Fatal("interval must be positive")
	}
	vcfg, err := loadVoiceConfig(*voiceConfig, *voiceEnabled)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c := collect.New()
	defer c.Close()
	var mp *magpie.Client
	if *magpieURL != "" {
		mp = magpie.New(*magpieURL)
	}

	if *dryRun {
		runDry(ctx, c, mp, *interval)
		return
	}

	control := &cliVoiceControl{}
	if vcfg.Enabled {
		log.Print("语音控制：wake / cancel / clear / preview [文字] / reference")
		go control.console(ctx, os.Stdin)
	}
	backoff := time.Second
	// CLI keeps the conversation across reconnects, in process memory only.
	memory := brain.OpenMemory("")
	for ctx.Err() == nil {
		target := *addr
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
		err = stream(attempt, c, mp, conn, *interval, *verbose, host)
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
				if err := conn.Send(ctx, qf); err != nil {
					return err
				}
				if verbose {
					log.Printf("sent quota frame: %d accounts", len(qf.Accounts))
				}
			}
		}
		f := c.Sample(ctx)
		host.setMetrics(f)
		if err := conn.Send(ctx, f); err != nil {
			return err
		}
		if verbose {
			b, _ := json.Marshal(f)
			log.Printf("sent %s", b)
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
