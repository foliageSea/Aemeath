// Package timeaware obtains an external UTC instant and projects it into a local timezone.
package timeaware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	endpoint       = "https://timeapi.io/api/time/current/zone?timeZone=UTC"
	provider       = "timeapi.io"
	requestTimeout = 4 * time.Second
	maxBodyBytes   = 32 * 1024
	maxResponseAge = 30 * time.Second
	fallbackNotice = "外部时间服务不可用，使用电脑系统时间"
)

// Info describes one instant projected into the requested timezone.
type Info struct {
	DateTime  string `json:"dateTime"`
	UTC       string `json:"utc"`
	Unix      int64  `json:"unix"`
	Timezone  string `json:"timezone"`
	UTCOffset string `json:"utcOffset"`
	Weekday   string `json:"weekday"`
	Date      string `json:"date"`
	Time      string `json:"time"`
	IsDST     bool   `json:"isDST"`
	Source    string `json:"source"`
	Provider  string `json:"provider"`
	Notice    string `json:"notice,omitempty"`
}

// Client is safe for concurrent calls after construction. Its dependencies are
// private so callers cannot redirect requests to arbitrary URLs.
type Client struct {
	httpClient *http.Client
	now        func() time.Time
	endpoint   string
	local      *time.Location
	timeout    time.Duration
}

// New constructs a client using a fixed HTTPS provider and the machine's local timezone.
func New() *Client {
	return &Client{
		httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:        time.Now, endpoint: endpoint, local: time.Local, timeout: requestTimeout,
	}
}

// Now obtains a fresh external instant. Service failures return explicitly
// labelled system time; cancellation of the caller's context always returns an error.
func (c *Client) Now(ctx context.Context, timezone string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	loc, err := c.location(timezone)
	if err != nil {
		return Info{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	instant, err := c.networkTime(requestCtx)
	if parentErr := ctx.Err(); parentErr != nil {
		return Info{}, parentErr
	}
	source, service, notice := "network", provider, ""
	if err != nil {
		instant = c.now().UTC()
		source, service, notice = "system_fallback", "system", fallbackNotice
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return Info{}, parentErr
	}
	result := formatInfo(instant, loc)
	result.Source, result.Provider, result.Notice = source, service, notice
	return result, nil
}

func (c *Client) location(name string) (*time.Location, error) {
	if len(name) > 128 {
		return nil, errors.New("invalid timezone: exceeds 128 bytes")
	}
	if name == "" || name == "Local" {
		return c.local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, errors.New("invalid timezone: expected an IANA timezone")
	}
	return loc, nil
}

func (c *Client) networkTime(ctx context.Context) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Cache-Control", "no-cache, no-store, max-age=0")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Accept", "application/json")
	// Enforce redirect refusal even when tests inject an HTTP client.
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}, errors.New("time service HTTP failure")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return time.Time{}, err
	}
	if len(body) > maxBodyBytes {
		return time.Time{}, errors.New("time service response too large")
	}
	var payload struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return time.Time{}, errors.New("invalid time service JSON")
	}
	if payload.TimeZone != "UTC" {
		return time.Time{}, errors.New("time service did not return UTC")
	}
	value := payload.DateTime
	if !strings.HasSuffix(value, "Z") {
		value += "Z"
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("invalid time service instant")
	}
	// Compare independent server metadata with the payload, never with the
	// machine clock: correcting an inaccurate machine clock is a primary use case.
	if date := resp.Header.Get("Date"); date != "" {
		serverDate, err := http.ParseTime(date)
		if err != nil {
			return time.Time{}, errors.New("invalid time service Date")
		}
		delta := instant.Sub(serverDate)
		if delta < -maxResponseAge || delta > maxResponseAge {
			return time.Time{}, errors.New("stale time service instant")
		}
	}
	if age := resp.Header.Get("Age"); age != "" {
		seconds, err := strconv.ParseUint(age, 10, 64)
		if err != nil || seconds > uint64(maxResponseAge/time.Second) {
			return time.Time{}, errors.New("stale time service response")
		}
	}
	return instant.UTC(), nil
}

func formatInfo(instant time.Time, loc *time.Location) Info {
	local := instant.In(loc)
	weekdays := [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	return Info{
		DateTime: local.Format(time.RFC3339Nano), UTC: instant.UTC().Format(time.RFC3339Nano),
		Unix: instant.Unix(), Timezone: loc.String(), UTCOffset: local.Format("-07:00"),
		Weekday: weekdays[local.Weekday()], Date: local.Format("2006-01-02"),
		Time: local.Format("15:04:05"), IsDST: local.IsDST(),
	}
}
