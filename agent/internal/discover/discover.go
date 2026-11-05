// Package discover finds the device on the LAN via mDNS.
package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	Service  = "_aemeath._tcp"
	Hostname = "aemeath.local"
)

// Find returns "host:port" of the first device answering, falling back to
// resolving aemeath.local through the OS resolver.
func Find(ctx context.Context, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if addr, err := browse(ctx); err == nil {
		return addr, nil
	}
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", Hostname)
	if err == nil && len(ips) > 0 {
		return net.JoinHostPort(ips[0].String(), "80"), nil
	}
	return "", errors.New("device not found via mDNS (" + Service + ") or " + Hostname)
}

func browse(ctx context.Context) (string, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return "", err
	}
	entries := make(chan *zeroconf.ServiceEntry)
	if err := resolver.Browse(ctx, Service, "local.", entries); err != nil {
		return "", err
	}
	for {
		select {
		case e, ok := <-entries:
			if !ok {
				return "", ctx.Err()
			}
			if len(e.AddrIPv4) > 0 {
				return net.JoinHostPort(e.AddrIPv4[0].String(), strconv.Itoa(e.Port)), nil
			}
		case <-ctx.Done():
			return "", fmt.Errorf("browse %s: %w", Service, ctx.Err())
		}
	}
}
