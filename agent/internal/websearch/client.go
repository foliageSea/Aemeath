// Package websearch retrieves untrusted search summaries from Tavily.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	defaultEndpoint = "https://api.tavily.com/search"
	maxBody         = 1 << 20
	requestTimeout  = 12 * time.Second
)

// Errors deliberately contain no API key, query, response body, or provider URL.
var (
	ErrQuery           = errors.New("websearch: invalid query")
	ErrLimit           = errors.New("websearch: invalid limit")
	ErrAPIKey          = errors.New("websearch: missing or invalid API key")
	ErrUnauthorized    = errors.New("websearch: unauthorized")
	ErrRateLimit       = errors.New("websearch: rate limit exceeded")
	ErrQuota           = errors.New("websearch: quota exceeded")
	ErrTransport       = errors.New("websearch: transport failure")
	ErrHTTPStatus      = errors.New("websearch: HTTP failure")
	ErrTooLarge        = errors.New("websearch: response too large")
	ErrInvalidResponse = errors.New("websearch: invalid response")
	ErrRedirect        = errors.New("websearch: redirect rejected")
)

type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}
type Response struct {
	Query   string   `json:"query"`
	Results []Result `json:"results"`
}

// Client is safe for concurrent searches after construction. Its endpoint and
// HTTP client are private injection points for package tests.
type Client struct {
	apiKey     string
	endpoint   string
	httpClient *http.Client
}

func New(apiKey string) *Client {
	// Reject line breaks before trimming so malformed credentials never reach HTTP.
	if len(apiKey) > 4096 || strings.ContainsAny(apiKey, "\r\n") {
		apiKey = ""
	}
	return &Client{apiKey: strings.TrimSpace(apiKey), endpoint: defaultEndpoint, httpClient: &http.Client{Timeout: requestTimeout}}
}

func normalizeQuery(query string) (string, error) {
	if !utf8.ValidString(query) {
		return "", ErrQuery
	}
	for _, r := range query {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return "", ErrQuery
		}
	}
	query = strings.Join(strings.Fields(query), " ")
	if n := utf8.RuneCountInString(query); n < 1 || n > 256 || len(query) > 1024 {
		return "", ErrQuery
	}
	return query, nil
}

// Search returns at most limit public source summaries. Zero selects three;
// explicit limits must be 1..5. Results are external, untrusted data, never code
// or instructions. No result page is fetched and no search is cached.
func (c *Client) Search(ctx context.Context, query string, limit int) (out Response, err error) {
	// Cancellation wins over validation, status, transport, and parsing errors.
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	query, err = normalizeQuery(query)
	if err != nil {
		return Response{}, err
	}
	if limit == 0 {
		limit = 3
	}
	if limit < 1 || limit > 5 {
		return Response{}, ErrLimit
	}
	if c.apiKey == "" || len(c.apiKey) > 4096 || strings.ContainsAny(c.apiKey, "\r\n") {
		return Response{}, ErrAPIKey
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			out, err = Response{}, ctx.Err()
		}
	}()
	payload := struct {
		Query             string `json:"query"`
		SearchDepth       string `json:"search_depth"`
		Topic             string `json:"topic"`
		MaxResults        int    `json:"max_results"`
		IncludeAnswer     bool   `json:"include_answer"`
		IncludeRawContent bool   `json:"include_raw_content"`
		AutoParameters    bool   `json:"auto_parameters"`
	}{Query: query, SearchDepth: "basic", Topic: "general", MaxResults: limit}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, ErrTransport
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Copy rather than mutate the injected client or any global HTTP client.
	client := *c.httpClient
	client.Timeout = requestTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrRedirect }
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case 401, 403:
		return Response{}, ErrUnauthorized
	case 429:
		return Response{}, ErrRateLimit
	case 402, 432, 433:
		return Response{}, ErrQuota
	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return Response{}, ErrRedirect
		}
		return Response{}, ErrHTTPStatus
	}
	if resp.ContentLength > maxBody {
		return Response{}, ErrTooLarge
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	if len(body) > maxBody {
		return Response{}, ErrTooLarge
	}
	return parseResponse(body, query, limit)
}

func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrRedirect) {
		return ErrRedirect
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return ErrTransport
}

func parseResponse(body []byte, query string, limit int) (Response, error) {
	// Require an actual result array; never treat a provider error payload as empty success.
	var envelope map[string]json.RawMessage
	if !utf8.Valid(body) || json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return Response{}, ErrInvalidResponse
	}
	for _, name := range []string{"error", "errors", "detail"} {
		if _, exists := envelope[name]; exists {
			return Response{}, ErrInvalidResponse
		}
	}
	raw, exists := envelope["results"]
	if !exists || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return Response{}, ErrInvalidResponse
	}
	var feed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &feed) != nil {
		return Response{}, ErrInvalidResponse
	}
	out := Response{Query: query, Results: make([]Result, 0, limit)}
	seen := make(map[string]bool)
	for _, item := range feed.Results {
		link := publicURL(item.URL)
		title := cleanText(item.Title, 100)
		if title == "" || link == "" || seen[link] {
			continue
		}
		seen[link] = true
		result := Result{Title: title, URL: link, Snippet: cleanText(item.Content, 240)}
		out.Results = append(out.Results, result)
		// encoding/json escapes HTML characters and controls; enforce the actual
		// serialized budget even for adversarial source URLs.
		encoded, _ := json.Marshal(out)
		if len(encoded) >= 16*1024 {
			out.Results = out.Results[:len(out.Results)-1]
			continue
		}
		if len(out.Results) == limit {
			break
		}
	}
	return out, nil
}

func cleanText(s string, limit int) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	hidden := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			text := strings.Join(strings.FieldsFunc(b.String(), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
			runes := []rune(text)
			if len(runes) > limit {
				text = string(runes[:limit])
			}
			return text
		case html.StartTagToken:
			t := z.Token()
			if t.Data == "script" || t.Data == "style" {
				hidden++
			}
			b.WriteByte(' ')
		case html.EndTagToken:
			t := z.Token()
			if (t.Data == "script" || t.Data == "style") && hidden > 0 {
				hidden--
			}
			b.WriteByte(' ')
		case html.SelfClosingTagToken:
			b.WriteByte(' ')
		case html.TextToken:
			if hidden == 0 {
				b.WriteString(z.Token().Data)
			}
		}
	}
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2002::/16"),
}

// ValidPublicURL reports whether raw is a public HTTP(S) source URL.
// It performs lexical validation only, with no DNS lookup or page fetch.
func ValidPublicURL(raw string) bool { return publicURL(raw) != "" }

// Host validation is lexical: it performs no DNS lookup or source-page fetch.
func publicURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 1024 {
		return ""
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.Contains(host, "%") {
		return ""
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
			return ""
		}
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return ""
		}
		for _, prefix := range nonPublic {
			if prefix.Contains(ip) {
				return ""
			}
		}
	} else {
		labels := strings.Split(host, ".")
		if len(labels) < 2 || len(host) > 253 {
			return ""
		}
		last := labels[len(labels)-1]
		// Reject alternate numeric IP spellings as well as local-use DNS suffixes.
		if last == "" || strings.Contains("0123456789", last[:1]) || strings.HasPrefix(last, "0x") {
			return ""
		}
		switch last {
		case "localhost", "local", "internal", "lan", "home", "test", "invalid", "onion":
			return ""
		}
		if host == "home.arpa" || strings.HasSuffix(host, ".home.arpa") {
			return ""
		}
		for _, label := range labels {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return ""
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
					return ""
				}
			}
		}
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	normalized := u.String()
	if len(normalized) > 1024 {
		return ""
	}
	return normalized
}
