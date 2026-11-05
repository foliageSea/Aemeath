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

	"aemeath/agent/internal/collect"
	"aemeath/agent/internal/discover"
	"aemeath/agent/internal/link"
)

func main() {
	addr := flag.String("addr", "", "device host:port (skips mDNS discovery)")
	interval := flag.Duration("interval", time.Second, "sample interval")
	verbose := flag.Bool("v", false, "log every frame")
	dryRun := flag.Bool("dry-run", false, "print frames to stdout instead of sending")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c := collect.New()
	defer c.Close()

	if *dryRun {
		runDry(ctx, c, *interval)
		return
	}

	backoff := time.Second
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
		err = stream(ctx, c, conn, *interval, *verbose)
		conn.Close()
		if ctx.Err() == nil {
			log.Printf("connection lost: %v", err)
		}
	}
}

func stream(ctx context.Context, c *collect.Collector, conn *link.Conn, interval time.Duration, verbose bool) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		f := c.Sample(ctx)
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

func runDry(ctx context.Context, c *collect.Collector, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
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
