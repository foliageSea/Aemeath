// Package collect samples host metrics into proto frames.
package collect

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"aemeath/agent/internal/proto"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
)

// sensors reads platform-specific values (temperatures, GPU). Implemented per OS.
type sensors interface {
	CPUTemp() *float64
	GPU() *proto.GPU
	Close()
}

type counters struct {
	at                  time.Time
	netTx, netRx        uint64
	diskRead, diskWrite uint64
}

type Collector struct {
	host     string
	cpuName  string
	diskPath string
	sens     sensors
	prev     *counters
}

func New() *Collector {
	c := &Collector{sens: newSensors(), diskPath: systemDiskPath()}
	c.host, _ = os.Hostname()
	c.host = strings.TrimSuffix(c.host, ".local")
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		c.cpuName = strings.TrimSpace(infos[0].ModelName)
	}
	cpu.Percent(0, true) // prime the per-core delta
	return c
}

func (c *Collector) Close() { c.sens.Close() }

func systemDiskPath() string {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("SystemDrive"); d != "" {
			return d + `\`
		}
		return `C:\`
	}
	// On macOS "/" is the sealed system snapshot; user data lives on the Data volume.
	if _, err := os.Stat("/System/Volumes/Data"); err == nil {
		return "/System/Volumes/Data"
	}
	return "/"
}

// Sample builds one frame. Rates use the delta since the previous call.
func (c *Collector) Sample(ctx context.Context) proto.Frame {
	now := time.Now()
	_, tz := now.Zone()
	f := proto.Frame{V: 1, Host: c.host, OS: runtime.GOOS, TS: now.Unix(), TZ: tz}

	f.CPU = &proto.CPU{Name: c.cpuName, Temp: c.sens.CPUTemp()}
	if cores, err := cpu.PercentWithContext(ctx, 0, true); err == nil && len(cores) > 0 {
		f.CPU.Cores = cores
		var sum float64
		for _, v := range cores {
			sum += v
		}
		f.CPU.Total = proto.F(sum / float64(len(cores)))
	}
	if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 && infos[0].Mhz > 0 {
		f.CPU.Freq = proto.F(infos[0].Mhz)
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		f.Mem = &proto.Mem{Used: vm.Used, Total: vm.Total}
		if sw, err := mem.SwapMemoryWithContext(ctx); err == nil && sw.Total > 0 {
			f.Mem.SwapUsed, f.Mem.SwapTotal = proto.U(sw.Used), proto.U(sw.Total)
		}
	}

	cur := counters{at: now}
	f.Disk = &proto.Disk{}
	if u, err := disk.UsageWithContext(ctx, c.diskPath); err == nil {
		f.Disk.Used, f.Disk.Total = proto.U(u.Used), proto.U(u.Total)
	}
	if io, err := disk.IOCountersWithContext(ctx); err == nil {
		for _, d := range io {
			cur.diskRead += d.ReadBytes
			cur.diskWrite += d.WriteBytes
		}
	}

	f.Net = &proto.Net{IP: primaryIP()}
	if ifs, err := psnet.IOCountersWithContext(ctx, true); err == nil {
		for _, i := range ifs {
			if physicalIface(i.Name) {
				cur.netTx += i.BytesSent
				cur.netRx += i.BytesRecv
			}
		}
	}

	if p := c.prev; p != nil {
		dt := cur.at.Sub(p.at).Seconds()
		f.Net.Up = rate(p.netTx, cur.netTx, dt)
		f.Net.Down = rate(p.netRx, cur.netRx, dt)
		f.Disk.Read = rate(p.diskRead, cur.diskRead, dt)
		f.Disk.Write = rate(p.diskWrite, cur.diskWrite, dt)
	}
	c.prev = &cur

	f.GPU = c.sens.GPU()
	if up, err := host.UptimeWithContext(ctx); err == nil {
		f.Uptime = proto.U(up)
	}
	return f
}

// rate returns bytes/s, or nil when no valid interval or the counter went backwards.
func rate(prev, cur uint64, dt float64) *float64 {
	if dt <= 0 || cur < prev {
		return nil
	}
	return proto.F(float64(cur-prev) / dt)
}

var virtualPrefixes = []string{
	"lo", "utun", "awdl", "llw", "bridge", "gif", "stf", "anpi", "ap", "vmnet", "vboxnet",
	"docker", "veth", "br-", "virbr", "tailscale", "zt",
	"Loopback", "vEthernet", "VMware", "VirtualBox", "Hyper-V", "isatap", "Teredo", "Bluetooth",
}

func physicalIface(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// primaryIP prefers a private IPv4 on a physical interface; proxy TUNs
// (e.g. fake-ip 198.18.0.0/15) would otherwise win the default route.
func primaryIP() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || !physicalIface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if ipn.IP.IsPrivate() {
				return ipn.IP.String()
			}
			if fallback == "" {
				fallback = ipn.IP.String()
			}
		}
	}
	return fallback
}
