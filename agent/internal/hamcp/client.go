// Package hamcp connects to Home Assistant's official MCP endpoint.
package hamcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultEndpoint = "http://homeassistant.local:8123/api/mcp/assist"
	maxBody         = 1 << 20
	maxTools        = 64
	maxSchema       = 32 << 10
	maxSchemas      = 256 << 10
	maxDescription  = 2048
	maxText         = 12 << 10
)

var (
	ErrUnauthorized = errors.New("Home Assistant MCP 认证失败")
	ErrUnavailable  = errors.New("Home Assistant MCP 暂时不可用")
	ErrProtocol     = errors.New("Home Assistant MCP 协议响应无效")
	ErrTooLarge     = errors.New("Home Assistant MCP 数据超过限制")
	ErrConfig       = errors.New("Home Assistant MCP 配置无效")
	ErrClosed       = errors.New("Home Assistant MCP 客户端已关闭")
	ErrUnknownTool  = errors.New("Home Assistant MCP 工具未发现")
)

type Config struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	Token    string `json:"-"`
}
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}
type Result struct {
	IsError bool   `json:"isError"`
	Text    string `json:"text"`
}
type Status struct {
	Connected bool   `json:"connected"`
	ToolCount int    `json:"toolCount"`
	Error     string `json:"error,omitempty"`
}

// NormalizeEndpoint accepts HA roots and explicit MCP endpoints, including a
// reverse proxy prefix. Arbitrary other paths are treated as HA proxy roots.
func NormalizeEndpoint(raw string) (string, error) {
	if raw == "" {
		return defaultEndpoint, nil
	}
	if len(raw) > 4096 || strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.TrimSpace(raw) != raw {
		return "", ErrConfig
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", ErrConfig
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "%\\ /?#@") {
		return "", ErrConfig
	}
	if strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(host); err != nil || !strings.HasPrefix(u.Host, "[") {
			return "", ErrConfig
		}
	} else {
		if strings.HasPrefix(u.Host, "[") {
			return "", ErrConfig
		}
		if ip, err := netip.ParseAddr(host); err != nil || !ip.Is4() {
			if len(host) > 253 {
				return "", ErrConfig
			}
			for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return "", ErrConfig
				}
				for _, r := range label {
					if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
						return "", ErrConfig
					}
				}
			}
		}
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return "", ErrConfig
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", ErrConfig
	}
	if strings.IndexFunc(u.Path, unicode.IsControl) >= 0 || strings.Contains(u.Path, "\\") || u.RawPath != "" {
		return "", ErrConfig
	}
	path := strings.TrimRight(u.Path, "/")
	i := strings.LastIndex(path, "/api/mcp")
	if i < 0 || (path[i:] != "/api/mcp" && !(strings.HasPrefix(path[i:], "/api/mcp/") && len(path[i+9:]) > 0 && !strings.Contains(path[i+9:], "/"))) {
		path += "/api/mcp/assist"
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return "", ErrConfig
		}
	}
	u.Path = path
	return u.String(), nil
}

// Client serializes operations; Close cancels in-flight requests before waiting
// for the operation gate. No SDK or server diagnostics leave this package.
type Client struct {
	config    Config
	root      context.Context
	cancel    context.CancelFunc
	gate      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	closeDone chan struct{}
	session   *mcp.ClientSession // protected by gate
	transport *safeTransport
	rawCount  atomic.Int64
	known     map[string]string // public redacted name -> wire name
}

func New(cfg Config) (*Client, error) {
	endpoint, err := NormalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if cfg.Enabled && (cfg.Token == "" || len(cfg.Token) > 16<<10 || strings.IndexFunc(cfg.Token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0) {
		return nil, ErrConfig
	}
	cfg.Endpoint = endpoint
	root, cancel := context.WithCancel(context.Background())
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // HA bearer credentials must go directly to the configured host.
	tr.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 5 * time.Second
	// The operation context supplies the full 8s/20s budget, including headers.
	tr.ResponseHeaderTimeout = 0
	c := &Client{config: cfg, root: root, cancel: cancel, gate: make(chan struct{}, 1), closeDone: make(chan struct{}), known: make(map[string]string)}
	c.transport = &safeTransport{base: tr, token: cfg.Token, root: root}
	return c, nil
}

func (c *Client) operation(parent context.Context, budget time.Duration) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	stop := context.AfterFunc(c.root, cancel)
	cleanup := func() { stop(); cancel() }
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		cleanup()
		if parent.Err() != nil {
			return nil, nil, parent.Err()
		}
		if c.closed.Load() {
			return nil, nil, ErrClosed
		}
		return nil, nil, ctx.Err()
	}
	if err := parent.Err(); err != nil {
		<-c.gate
		cleanup()
		return nil, nil, err
	}
	if c.closed.Load() {
		<-c.gate
		cleanup()
		return nil, nil, ErrClosed
	}
	if !c.config.Enabled {
		<-c.gate
		cleanup()
		return nil, nil, ErrConfig
	}
	if ctx.Err() != nil {
		<-c.gate
		cleanup()
		return nil, nil, ctx.Err()
	}
	c.transport.reset(ctx)
	return ctx, func() { <-c.gate; cleanup() }, nil
}

func (c *Client) connect(ctx context.Context) error {
	sdk := mcp.NewClient(&mcp.Implementation{Name: "aemeath-ha", Version: "1"}, &mcp.ClientOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Capabilities: &mcp.ClientCapabilities{}})
	// Inspect the unfiltered tools/list count: the SDK otherwise silently drops
	// tools with malformed header annotations, which must be an explicit error.
	sdk.AddSendingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			// New protocol sessions cache lists; reject an unexpected upgrade.
			if init, ok := res.(*mcp.InitializeResult); ok && init != nil && init.ProtocolVersion >= "2026-07-28" {
				return nil, ErrProtocol
			}
			if list, ok := res.(*mcp.ListToolsResult); ok && list != nil {
				c.rawCount.Store(int64(len(list.Tools)))
			}
			return res, err
		}
	})
	session, err := sdk.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.config.Endpoint, HTTPClient: &http.Client{Transport: c.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: maxBody}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-03-26"})
	if err != nil {
		return err
	}
	c.session = session
	return nil
}

// Discover always fetches a fresh, complete list. Failed discovery revokes the
// previous allowlist and tears down the session so the next attempt reconnects.
func (c *Client) Discover(parent context.Context) (out []Tool, err error) {
	ctx, done, err := c.operation(parent, 8*time.Second)
	if err != nil {
		return nil, err
	}
	defer done()
	c.known = make(map[string]string)
	defer func() {
		if err != nil {
			c.dropSession()
			err = c.safeError(parent, ctx, err)
		}
	}()
	if c.session == nil {
		if err = c.connect(ctx); err != nil {
			return nil, err
		}
	}
	out = make([]Tool, 0)
	names := make(map[string]string)
	cursors := make(map[string]bool)
	cursor := ""
	total := 0
	for pages := 0; ; pages++ {
		if pages >= maxTools+1 {
			return nil, ErrTooLarge
		}
		c.rawCount.Store(-1)
		var page *mcp.ListToolsResult
		page, err = c.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if page == nil || int(c.rawCount.Load()) != len(page.Tools) {
			return nil, ErrProtocol
		}
		if len(out)+len(page.Tools) > maxTools {
			return nil, ErrTooLarge
		}
		for _, t := range page.Tools {
			if t == nil || t.Name == "" {
				return nil, ErrProtocol
			}
			if len(t.Description) > maxDescription {
				return nil, ErrTooLarge
			}
			schema, e := json.Marshal(t.InputSchema)
			if e != nil || len(schema) == 0 || schema[0] != '{' {
				return nil, ErrProtocol
			}
			if len(schema) > maxSchema {
				return nil, ErrTooLarge
			}
			originalSize := len(schema)
			name := c.redact(t.Name)
			if _, exists := names[name]; exists {
				return nil, ErrProtocol
			}
			title := t.Title
			if title == "" && t.Annotations != nil {
				title = t.Annotations.Title
			}
			schema, e = redactJSON(schema, c.config.Token)
			if e != nil {
				return nil, ErrProtocol
			}
			if len(schema) > maxSchema {
				return nil, ErrTooLarge
			}
			// Enforce limits both before and after credential redaction.
			total += max(originalSize, len(schema))
			description := c.redact(t.Description)
			if total > maxSchemas || len(description) > maxDescription {
				return nil, ErrTooLarge
			}
			out = append(out, Tool{Name: name, Title: c.redact(title), Description: description, InputSchema: schema})
			names[name] = t.Name
		}
		if page.NextCursor == "" {
			break
		}
		if cursors[page.NextCursor] {
			return nil, ErrProtocol
		}
		cursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	c.known = names
	return out, nil
}

// Call sends a discovered tool exactly once. Transport failure makes the action
// outcome uncertain; only a later explicit Discover may establish a new session.
func (c *Client) Call(parent context.Context, name string, args json.RawMessage) (out Result, err error) {
	ctx, done, err := c.operation(parent, 20*time.Second)
	if err != nil {
		return out, err
	}
	defer done()
	wire, ok := c.known[name]
	if !ok || c.session == nil {
		return out, ErrUnknownTool
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if len(args) > maxBody {
		return out, ErrTooLarge
	}
	trimmed := strings.TrimSpace(string(args))
	if !json.Valid(args) || len(trimmed) == 0 || trimmed[0] != '{' {
		return out, ErrProtocol
	}
	res, e := c.session.CallTool(ctx, &mcp.CallToolParams{Name: wire, Arguments: args})
	if e != nil {
		c.known = make(map[string]string)
		c.dropSession()
		err = c.safeError(parent, ctx, e)
		return out, err
	}
	if res == nil || res.NeedsInput() {
		return out, ErrProtocol
	}
	var parts []string
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok && text != nil {
			parts = append(parts, text.Text)
		}
	}
	if res.StructuredContent != nil {
		data, e := json.Marshal(res.StructuredContent)
		if e != nil {
			return out, ErrProtocol
		}
		data, e = redactJSON(data, c.config.Token)
		if e != nil {
			return out, ErrProtocol
		}
		parts = append(parts, string(data))
	}
	out = Result{IsError: res.IsError, Text: truncate(c.redact(strings.Join(parts, "\n")), maxText)}
	return out, nil
}

func (c *Client) dropSession() {
	if c.session != nil {
		_ = c.session.Close()
		c.session = nil
	}
	c.transport.base.CloseIdleConnections()
}
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.cancel()
		c.gate <- struct{}{}
		c.known = nil
		c.dropSession()
		<-c.gate
		close(c.closeDone)
	})
	<-c.closeDone
	return nil
}
func (c *Client) redact(s string) string {
	if c.config.Token != "" {
		return strings.ReplaceAll(s, c.config.Token, "[已隐藏]")
	}
	return s
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	suffix := "\n[结果已截断]"
	end := n - len(suffix)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + suffix
}

// Redact decoded JSON strings (including keys) so escaped token spellings cannot
// bypass redaction, and preserve valid JSON after replacing quotes/backslashes.
func redactJSON(data []byte, token string) ([]byte, error) {
	var value any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	collision := false
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if token != "" {
				return strings.ReplaceAll(x, token, "[已隐藏]")
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			m := make(map[string]any, len(x))
			for k, v := range x {
				key := k
				if token != "" {
					key = strings.ReplaceAll(k, token, "[已隐藏]")
				}
				if _, exists := m[key]; exists {
					collision = true
				}
				m[key] = walk(v)
			}
			return m
		}
		return v
	}
	value = walk(value)
	if collision {
		return nil, ErrProtocol
	}
	return json.Marshal(value)
}
func (c *Client) safeError(parent, ctx context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if c.closed.Load() {
		return ErrClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, known := range []error{ErrUnauthorized, ErrUnavailable, ErrProtocol, ErrTooLarge, ErrConfig, ErrUnknownTool} {
		if errors.Is(err, known) {
			return known
		}
	}
	if e := c.transport.failure(); e != nil {
		return e
	}
	return ErrProtocol
}

// ErrorMessage deliberately never includes the original diagnostic or URL.
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	for _, known := range []error{ErrUnauthorized, ErrUnavailable, ErrProtocol, ErrTooLarge, ErrConfig, ErrClosed, ErrUnknownTool} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, context.Canceled) {
		return "Home Assistant MCP 请求已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Home Assistant MCP 请求超时"
	}
	return "Home Assistant MCP 请求失败"
}

// safeTransport bounds bytes while streaming, adds credentials to every request,
// and records only stable categories. It never buffers an unbounded SSE body.
type safeTransport struct {
	base      *http.Transport
	token     string
	root      context.Context
	mu        sync.Mutex
	err       error
	operation context.Context
}

func (t *safeTransport) reset(ctx context.Context) {
	t.mu.Lock()
	t.err = nil
	t.operation = ctx
	t.mu.Unlock()
}
func (t *safeTransport) record(err error) {
	t.mu.Lock()
	if t.err == nil {
		t.err = err
	}
	t.mu.Unlock()
}
func (t *safeTransport) failure() error { t.mu.Lock(); defer t.mu.Unlock(); return t.err }
func (t *safeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.root, cancel)
	t.mu.Lock()
	op := t.operation
	t.mu.Unlock()
	stopOperation := func() bool { return false }
	if op != nil {
		stopOperation = context.AfterFunc(op, cancel)
	}
	cleanup := func() { stop(); stopOperation(); cancel() }
	if op != nil && op.Err() != nil {
		cleanup()
		return nil, op.Err()
	}
	req = req.Clone(ctx)
	req.Header.Set("Authorization", "Bearer "+t.token)
	if t.root.Err() != nil {
		cleanup()
		return nil, ErrClosed
	}
	// Never allow net/http to replay an action on a stale reused connection.
	req.GetBody = nil
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		cleanup()
		t.record(ErrUnavailable)
		return nil, ErrUnavailable
	}
	category := error(nil)
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		category = ErrUnauthorized
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		category = ErrProtocol
	case resp.StatusCode == 413:
		category = ErrTooLarge
	case resp.StatusCode >= 500:
		category = ErrUnavailable
	case resp.StatusCode >= 400:
		category = ErrProtocol
	}
	if category != nil {
		resp.Body.Close()
		cleanup()
		t.record(category)
		return nil, category
	}
	if resp.ContentLength > maxBody {
		resp.Body.Close()
		cleanup()
		t.record(ErrTooLarge)
		return nil, ErrTooLarge
	}
	resp.Body = &limitedBody{ReadCloser: resp.Body, remaining: maxBody, cleanup: cleanup, record: t.record}
	return resp, nil
}

type limitedBody struct {
	io.ReadCloser
	remaining int64
	cleanup   func()
	record    func(error)
	once      sync.Once
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.remaining {
		b.record(ErrTooLarge)
		b.Close()
		return 0, ErrTooLarge
	}
	b.remaining -= int64(n)
	if err != nil && err != io.EOF {
		b.record(ErrUnavailable)
	}
	return n, err
}
func (b *limitedBody) Close() error {
	var err error
	b.once.Do(func() { b.cleanup(); err = b.ReadCloser.Close() })
	return err
}
