// Package proto defines the JSON frame pushed to the device over WebSocket.
// Field names must match firmware/main/metrics.c. Unavailable values are omitted.
package proto

type Frame struct {
	V      int     `json:"v"`
	Host   string  `json:"host"`
	OS     string  `json:"os"`
	TS     int64   `json:"ts"`
	TZ     int     `json:"tz"`
	CPU    *CPU    `json:"cpu,omitempty"`
	Mem    *Mem    `json:"mem,omitempty"`
	Disk   *Disk   `json:"disk,omitempty"`
	Net    *Net    `json:"net,omitempty"`
	GPU    *GPU    `json:"gpu,omitempty"`
	Uptime *uint64 `json:"uptime,omitempty"`
}

type CPU struct {
	Total *float64  `json:"total,omitempty"` // %
	Cores []float64 `json:"cores,omitempty"` // % per logical core
	Freq  *float64  `json:"freq,omitempty"`  // MHz
	Temp  *float64  `json:"temp,omitempty"`  // °C
	Name  string    `json:"name,omitempty"`
}

type Mem struct {
	Used      uint64  `json:"used"`
	Total     uint64  `json:"total"`
	SwapUsed  *uint64 `json:"swap_used,omitempty"`
	SwapTotal *uint64 `json:"swap_total,omitempty"`
}

type Disk struct {
	Used  *uint64  `json:"used,omitempty"`
	Total *uint64  `json:"total,omitempty"`
	Read  *float64 `json:"rd,omitempty"` // bytes/s
	Write *float64 `json:"wr,omitempty"` // bytes/s
}

type Net struct {
	Up   *float64 `json:"up,omitempty"`   // bytes/s
	Down *float64 `json:"down,omitempty"` // bytes/s
	IP   string   `json:"ip,omitempty"`
}

type GPU struct {
	Name     string   `json:"name,omitempty"`
	Load     *float64 `json:"load,omitempty"` // %
	Temp     *float64 `json:"temp,omitempty"` // °C
	MemUsed  *uint64  `json:"mem_used,omitempty"`
	MemTotal *uint64  `json:"mem_total,omitempty"`
}

func F(v float64) *float64 { return &v }
func U(v uint64) *uint64   { return &v }

// QuotaFrame carries quotas and balances read from the Magpie gateway.
type QuotaFrame struct {
	V        int            `json:"v"`
	Type     string         `json:"type"` // "quota"
	TS       int64          `json:"ts"`
	Accounts []QuotaAccount `json:"accounts"`
	Usage    *QuotaUsage    `json:"usage,omitempty"`
}

type QuotaAccount struct {
	Name    string        `json:"name"`
	Plan    string        `json:"plan,omitempty"`
	Kind    string        `json:"kind,omitempty"`
	Balance string        `json:"balance,omitempty"`
	Windows []QuotaWindow `json:"windows"`
}

type QuotaWindow struct {
	Name    string  `json:"name"`
	Used    float64 `json:"used"`              // % of the window consumed
	Reset   int64   `json:"reset,omitempty"`   // unix seconds
	Display string  `json:"display,omitempty"` // e.g. "3535 / 7000"
}

type QuotaUsage struct {
	CostToday  float64 `json:"cost_today"`
	CallsToday int64   `json:"calls_today"`
	Cost30d    float64 `json:"cost_30d"`
	Calls30d   int64   `json:"calls_30d"`
}
