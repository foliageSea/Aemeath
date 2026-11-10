package desktop

import (
	"aemeath/agent/internal/proto"
	"aemeath/agent/internal/voice"
)

// State is an owned snapshot: callers may modify any nested value safely.
type State struct {
	Running    bool              `json:"running"`
	Connected  bool              `json:"connected"`
	Status     string            `json:"status"`
	Target     string            `json:"target"`
	Error      string            `json:"error"`
	QuotaError string            `json:"quotaError"`
	Metrics    *proto.Frame      `json:"metrics"`
	Quota      *proto.QuotaFrame `json:"quota"`
	Settings   Settings          `json:"settings"`
	Page       string            `json:"page"`
	Voice      voice.State       `json:"voice"`
	// Screen state as last reported by the device; ScreenKnown is false until the
	// connected device reports (ScreenOn/Brightness are then only placeholders).
	ScreenOn    bool  `json:"screenOn"`
	Brightness  int   `json:"brightness"`
	ScreenKnown bool  `json:"screenKnown"`
	LastSent    int64 `json:"lastSent"`
}

// Clone returns a snapshot with independently owned nested values.
func (s State) Clone() State {
	s.Settings.Subscriptions = append([]Subscription{}, s.Settings.Subscriptions...)
	s.Metrics = cloneMetrics(s.Metrics)
	s.Quota = cloneQuota(s.Quota)
	s.Voice = s.Voice.Clone()
	return s
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func cloneMetrics(f *proto.Frame) *proto.Frame {
	c := copyPtr(f)
	if c == nil {
		return nil
	}
	c.CPU = copyPtr(f.CPU)
	if c.CPU != nil {
		c.CPU.Total = copyPtr(f.CPU.Total)
		c.CPU.Freq = copyPtr(f.CPU.Freq)
		c.CPU.Temp = copyPtr(f.CPU.Temp)
		c.CPU.Cores = append([]float64(nil), f.CPU.Cores...)
	}
	c.Mem = copyPtr(f.Mem)
	if c.Mem != nil {
		c.Mem.SwapUsed = copyPtr(f.Mem.SwapUsed)
		c.Mem.SwapTotal = copyPtr(f.Mem.SwapTotal)
	}
	c.Disk = copyPtr(f.Disk)
	if c.Disk != nil {
		c.Disk.Used = copyPtr(f.Disk.Used)
		c.Disk.Total = copyPtr(f.Disk.Total)
		c.Disk.Read = copyPtr(f.Disk.Read)
		c.Disk.Write = copyPtr(f.Disk.Write)
	}
	c.Net = copyPtr(f.Net)
	if c.Net != nil {
		c.Net.Up = copyPtr(f.Net.Up)
		c.Net.Down = copyPtr(f.Net.Down)
	}
	c.GPU = copyPtr(f.GPU)
	if c.GPU != nil {
		c.GPU.Load = copyPtr(f.GPU.Load)
		c.GPU.Temp = copyPtr(f.GPU.Temp)
		c.GPU.MemUsed = copyPtr(f.GPU.MemUsed)
		c.GPU.MemTotal = copyPtr(f.GPU.MemTotal)
	}
	c.Uptime = copyPtr(f.Uptime)
	return c
}
func cloneQuota(f *proto.QuotaFrame) *proto.QuotaFrame {
	c := copyPtr(f)
	if c == nil {
		return nil
	}
	c.Accounts = append([]proto.QuotaAccount{}, f.Accounts...)
	for i := range c.Accounts {
		c.Accounts[i].Windows = append([]proto.QuotaWindow{}, f.Accounts[i].Windows...)
	}
	c.Usage = copyPtr(f.Usage)
	return c
}

// orderedQuota applies preferences without losing accounts in the UI snapshot.
// Unlisted accounts follow configured accounts in gateway order.
func orderedQuota(f *proto.QuotaFrame, preferences []Subscription) *proto.QuotaFrame {
	out := cloneQuota(f)
	if out == nil {
		return nil
	}
	accounts := out.Accounts
	out.Accounts = []proto.QuotaAccount{}
	seen := map[string]bool{}
	for _, preference := range preferences {
		seen[preference.Name] = true
		if preference.Hidden {
			continue
		}
		for _, account := range accounts {
			if account.Name == preference.Name {
				out.Accounts = append(out.Accounts, account)
			}
		}
	}
	for _, account := range accounts {
		if !seen[account.Name] {
			out.Accounts = append(out.Accounts, account)
		}
	}
	return out
}
