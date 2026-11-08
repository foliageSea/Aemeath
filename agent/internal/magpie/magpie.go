// Package magpie reads quotas and balances from the local Magpie gateway.
package magpie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"aemeath/agent/internal/proto"
)

const (
	DefaultBase = "http://127.0.0.1:3425"
	maxAccounts = 8
	maxWindows  = 8
	maxName     = 31
	maxDisplay  = 15
	maxBalance  = 63 // firmware balance buffer includes a trailing NUL
)

type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 5 * time.Second}}
}

type quotasResp struct {
	Data []struct {
		Name    string `json:"name"`
		Plan    string `json:"plan"`
		Kind    string `json:"kind"`
		Balance string `json:"balance"`
		Windows []struct {
			Name      string  `json:"name"`
			Used      float64 `json:"used"`
			ResetsAt  string  `json:"resetsAt"`
			Display   string  `json:"display"`
			Unlimited bool    `json:"unlimited"`
		} `json:"windows"`
	} `json:"data"`
}

type usageResp struct {
	Calls  int64   `json:"calls"`
	Cost   float64 `json:"cost"`
	Series []struct {
		Calls int64   `json:"calls"`
		Cost  float64 `json:"cost"`
	} `json:"series"`
}

func (c *Client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Frame fetches quotas (required) and usage totals (best effort).
func (c *Client) Frame(ctx context.Context) (*proto.QuotaFrame, error) {
	var q quotasResp
	if err := c.get(ctx, "/v1/magpie/quotas", &q); err != nil {
		return nil, err
	}
	f := &proto.QuotaFrame{V: 1, Type: "quota", TS: time.Now().Unix(), Accounts: []proto.QuotaAccount{}}
	for _, a := range q.Data {
		if len(f.Accounts) == maxAccounts {
			break
		}
		acc := proto.QuotaAccount{
			Name:    ascii(a.Name, maxName),
			Plan:    ascii(a.Plan, maxName),
			Kind:    a.Kind,
			Balance: ascii(a.Balance, maxBalance),
			Windows: []proto.QuotaWindow{},
		}
		for _, w := range a.Windows {
			if w.Unlimited {
				continue
			}
			if len(acc.Windows) == maxWindows {
				break
			}
			win := proto.QuotaWindow{Name: ascii(w.Name, maxName), Used: w.Used, Display: ascii(w.Display, maxDisplay)}
			if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
				win.Reset = t.Unix()
			}
			acc.Windows = append(acc.Windows, win)
		}
		f.Accounts = append(f.Accounts, acc)
	}

	var u usageResp
	if err := c.get(ctx, "/v1/magpie/usage", &u); err == nil {
		f.Usage = &proto.QuotaUsage{Cost30d: u.Cost, Calls30d: u.Calls}
		if n := len(u.Series); n > 0 {
			f.Usage.CostToday, f.Usage.CallsToday = u.Series[n-1].Cost, u.Series[n-1].Calls
		}
	}
	return f, nil
}

// ascii folds text to what the device font can render.
func ascii(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '·' || r == '–' || r == '—':
			b.WriteByte('-')
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) > limit {
		out = strings.TrimSpace(out[:limit])
	}
	return out
}
