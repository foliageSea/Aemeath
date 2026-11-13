package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"aemeath/agent/internal/background"
)

var (
	ErrBackgroundUnsupported = errors.New("设备不支持背景功能，请升级固件和分区表")
	ErrBackgroundConflict    = errors.New("背景已被其他操作修改，请刷新后重试")
	ErrBackgroundBusy        = errors.New("设备正忙，请稍后重试")
	ErrBackgroundNoImage     = errors.New("设备尚未设置背景图片")
)

const backgroundJSONLimit = 4096

type backgroundSession struct {
	service       *Service
	conn          connection
	target        string
	ctx           context.Context
	connectionCtx context.Context
	serviceCtx    context.Context
	client        http.Client
	release       func()
}

// backgroundClient captures identity, not a live request: it owns no goroutine,
// waiter or socket, and can be reused until that exact connection ends.
type backgroundClient struct {
	service *Service
	conn    connection
	connCtx context.Context
	target  string
}

// Current lets UI completions reject results after a same-address reconnect,
// including a reconnect after the HTTP method already returned successfully.
// The handle is connection-scoped, not scoped to any individual request context.
func (b *backgroundClient) Current() bool {
	if b == nil || b.service == nil || b.connCtx == nil {
		return false
	}
	s := b.service
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed && s.state.Connected && s.conn == b.conn && s.connCtx == b.connCtx && s.state.Target == b.target && s.ctx != nil && s.ctx.Err() == nil && b.connCtx.Err() == nil
}

// BackgroundClient binds synchronously to the device the UI intends to edit.
// Call this before dispatching asynchronous work, not inside its goroutine.
func (s *Service) BackgroundClient(expectedTarget string) (background.Client, error) {
	return s.bindBackground(&expectedTarget)
}

func (s *Service) bindBackground(expectedTarget *string) (*backgroundClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.conn == nil || !s.state.Connected || s.connCtx == nil || s.ctx == nil || s.state.Target == "" {
		return nil, ErrOffline
	}
	if expectedTarget != nil && s.state.Target != *expectedTarget {
		return nil, ErrOffline
	}
	if err := validateAddress(s.state.Target); err != nil {
		return nil, fmt.Errorf("设备地址无效：%w", err)
	}
	if err := s.connCtx.Err(); err != nil {
		return nil, err
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	return &backgroundClient{service: s, conn: s.conn, connCtx: s.connCtx, target: s.state.Target}, nil
}

func (s *Service) backgroundForRequest(request context.Context) (*backgroundClient, error) {
	if request == nil {
		return nil, fmt.Errorf("背景请求上下文不能为空")
	}
	if err := request.Err(); err != nil {
		return nil, err
	}
	return s.bindBackground(nil)
}

// beginBackground is the current-device convenience path. UI mutations use a
// pre-bound background.Client instead, so delayed goroutine admission is safe.
func (s *Service) beginBackground(request context.Context) (*backgroundSession, error) {
	client, err := s.backgroundForRequest(request)
	if err != nil {
		return nil, err
	}
	return client.beginBackground(request)
}

// Admission and wg.Add share the lock Close uses to seal admission. Crucially,
// the captured identity is checked here, not just when creating the UI handle.
func (b *backgroundClient) beginBackground(request context.Context) (*backgroundSession, error) {
	if request == nil {
		return nil, fmt.Errorf("背景请求上下文不能为空")
	}
	if err := request.Err(); err != nil {
		return nil, err
	}
	s := b.service
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if !s.state.Connected || s.conn != b.conn || s.connCtx != b.connCtx || s.state.Target != b.target {
		s.mu.Unlock()
		return nil, ErrOffline
	}
	connectionCtx, serviceCtx := b.connCtx, s.ctx
	if err := connectionCtx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := serviceCtx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	client := http.Client{}
	if s.deps.backgroundHTTP != nil {
		client = *s.deps.backgroundHTTP
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 30 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s.wg.Add(1)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(request, 30*time.Second)
	stopConn := context.AfterFunc(connectionCtx, cancel)
	stopService := context.AfterFunc(serviceCtx, cancel)
	session := &backgroundSession{service: s, conn: b.conn, target: b.target, ctx: ctx, connectionCtx: connectionCtx, serviceCtx: serviceCtx, client: client}
	session.release = func() { stopConn(); stopService(); cancel(); s.wg.Done() }
	if err := session.current(); err != nil {
		session.release()
		return nil, err
	}
	return session, nil
}

func (b *backgroundSession) current() error {
	if err := b.connectionCtx.Err(); err != nil {
		return err
	}
	if err := b.serviceCtx.Err(); err != nil {
		return err
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	b.service.mu.RLock()
	defer b.service.mu.RUnlock()
	if b.service.closed {
		return ErrClosed
	}
	if !b.service.state.Connected || b.service.conn != b.conn || b.service.connCtx != b.connectionCtx || b.service.state.Target != b.target {
		return ErrOffline
	}
	return nil
}

func (b *backgroundSession) request(method, path string, body []byte, expected *uint64) (*http.Response, error) {
	if err := b.current(); err != nil {
		return nil, err
	}
	endpoint := url.URL{Scheme: "http", Host: deviceAddress(b.target), Path: path}
	req, err := http.NewRequestWithContext(b.ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if path == "/api/background/image" {
		req.Header.Set("Accept", "image/jpeg")
	}
	if expected != nil {
		req.Header.Set("If-Match", strconv.Quote(strconv.FormatUint(*expected, 10)))
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "image/jpeg")
		sum := sha256.Sum256(body)
		req.Header.Set("X-Content-SHA256", hex.EncodeToString(sum[:]))
	}
	// Deliberately no request retry, including an ambiguous write timeout.
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("背景请求失败：%w", err)
	}
	return resp, nil
}

func boundedBackgroundBody(resp *http.Response, limit int) ([]byte, error) {
	if resp.ContentLength > int64(limit) {
		return nil, fmt.Errorf("设备背景响应超过大小限制")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("读取背景响应：%w", err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("设备背景响应超过大小限制")
	}
	if resp.ContentLength >= 0 && int64(len(data)) != resp.ContentLength {
		return nil, fmt.Errorf("设备背景响应长度不一致")
	}
	return data, nil
}

func backgroundContentType(resp *http.Response, want string) bool {
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && media == want
}

func backgroundStatusError(resp *http.Response, image bool) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusNotFound:
		if image {
			return ErrBackgroundNoImage
		}
		return ErrBackgroundUnsupported
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return ErrBackgroundUnsupported
	case http.StatusPreconditionFailed:
		return ErrBackgroundConflict
	case http.StatusConflict:
		return ErrBackgroundBusy
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("背景图片超过设备大小上限")
	case http.StatusUnsupportedMediaType:
		return fmt.Errorf("设备仅接受 baseline JPEG 背景")
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("设备拒绝背景格式、尺寸或校验信息")
	case http.StatusServiceUnavailable:
		return fmt.Errorf("设备背景资源不足或暂不可用")
	default:
		return fmt.Errorf("设备背景请求返回 HTTP %d", resp.StatusCode)
	}
}

func decodeBackgroundInfo(data []byte, etag string) (background.Info, error) {
	var info background.Info
	if !utf8.Valid(data) {
		return info, fmt.Errorf("设备背景状态不是有效 UTF-8")
	}
	invalid := func() (background.Info, error) {
		return background.Info{}, fmt.Errorf("设备背景状态响应无效")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return invalid()
	}
	required := map[string]bool{"v": false, "supported": false, "present": false, "displayed": false, "revision": false, "sha256": false, "width": false, "height": false, "bytes": false, "maxBytes": false, "scope": false}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return invalid()
		}
		if _, ok := required[name]; !ok && name != "error" {
			return invalid()
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid()
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return invalid()
	}
	if _, err = decoder.Token(); err != io.EOF {
		return invalid()
	}
	for key := range required {
		if !seen[key] {
			return invalid()
		}
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return invalid()
	}
	if info.V != 1 || info.MaxBytes != background.MaxBytes || info.Scope != "shared" || len(info.Error) > 1024 {
		return invalid()
	}
	if etag != strconv.Quote(strconv.FormatUint(info.Revision, 10)) {
		return invalid()
	}
	if info.Present {
		hash, err := hex.DecodeString(info.SHA256)
		if err != nil || len(hash) != sha256.Size || strings.ToLower(info.SHA256) != info.SHA256 || info.Width != background.Size || info.Height != background.Size || info.Bytes <= 0 || info.Bytes > background.MaxBytes || info.Revision == 0 {
			return invalid()
		}
	} else if info.Displayed || info.Width != 0 || info.Height != 0 || info.Bytes != 0 || info.SHA256 != "" {
		return invalid()
	}
	if !info.Supported && (info.Present || info.Displayed) {
		return invalid()
	}
	return info, nil
}

func (b *backgroundSession) info(method string, body []byte, expected *uint64) (background.Info, error) {
	resp, err := b.request(method, "/api/background", body, expected)
	if err != nil {
		return background.Info{}, err
	}
	defer resp.Body.Close()
	// Read even error bodies with a small bound, but never reflect arbitrary HTML
	// or secrets from device responses into user-facing errors.
	data, readErr := boundedBackgroundBody(resp, backgroundJSONLimit)
	if err := backgroundStatusError(resp, false); err != nil {
		return background.Info{}, err
	}
	if readErr != nil {
		return background.Info{}, readErr
	}
	if resp.StatusCode != http.StatusOK && (method != http.MethodPut || resp.StatusCode != http.StatusCreated) {
		return background.Info{}, fmt.Errorf("设备背景响应状态无效")
	}
	if backgroundContentType(resp, "text/html") || bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		return background.Info{}, ErrBackgroundUnsupported
	}
	if !backgroundContentType(resp, "application/json") {
		return background.Info{}, fmt.Errorf("设备背景状态 Content-Type 无效")
	}
	info, err := decodeBackgroundInfo(data, resp.Header.Get("ETag"))
	if err != nil {
		return background.Info{}, err
	}
	if err := b.current(); err != nil {
		return background.Info{}, err
	}
	if !info.Supported {
		return info, ErrBackgroundUnsupported
	}
	return info, nil
}

// Convenience methods atomically choose the current device. UI code with an
// existing device snapshot must use BackgroundClient(expectedTarget) instead.
func (s *Service) GetBackground(ctx context.Context) (background.Info, error) {
	client, err := s.backgroundForRequest(ctx)
	if err != nil {
		return background.Info{}, err
	}
	return client.GetBackground(ctx)
}
func (s *Service) GetBackgroundImage(ctx context.Context) ([]byte, error) {
	client, err := s.backgroundForRequest(ctx)
	if err != nil {
		return nil, err
	}
	return client.GetBackgroundImage(ctx)
}
func (s *Service) SetBackground(ctx context.Context, jpeg []byte, expected uint64) (background.Info, error) {
	client, err := s.backgroundForRequest(ctx)
	if err != nil {
		return background.Info{}, err
	}
	return client.SetBackground(ctx, jpeg, expected)
}
func (s *Service) ClearBackground(ctx context.Context, expected uint64) (background.Info, error) {
	client, err := s.backgroundForRequest(ctx)
	if err != nil {
		return background.Info{}, err
	}
	return client.ClearBackground(ctx, expected)
}

func (s *backgroundClient) GetBackground(ctx context.Context) (background.Info, error) {
	session, err := s.beginBackground(ctx)
	if err != nil {
		return background.Info{}, err
	}
	defer session.release()
	return session.info(http.MethodGet, nil, nil)
}

// GetBackgroundImage verifies both the bounded JPEG and the current status hash.
// A concurrent replacement is an error, never a preview of the wrong revision.
func (s *backgroundClient) GetBackgroundImage(ctx context.Context) ([]byte, error) {
	session, err := s.beginBackground(ctx)
	if err != nil {
		return nil, err
	}
	defer session.release()
	info, err := session.info(http.MethodGet, nil, nil)
	if err != nil {
		return nil, err
	}
	if !info.Present {
		return nil, ErrBackgroundNoImage
	}
	resp, err := session.request(http.MethodGet, "/api/background/image", nil, &info.Revision)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, readErr := boundedBackgroundBody(resp, background.MaxBytes)
	if err := backgroundStatusError(resp, true); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("设备背景图片响应状态无效")
	}
	if backgroundContentType(resp, "text/html") || bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		return nil, ErrBackgroundUnsupported
	}
	if !backgroundContentType(resp, "image/jpeg") {
		return nil, fmt.Errorf("设备背景图片 Content-Type 无效")
	}
	if err := background.ValidateJPEG(data); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if len(data) != info.Bytes || hex.EncodeToString(sum[:]) != info.SHA256 {
		return nil, ErrBackgroundConflict
	}
	if err := session.current(); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *backgroundClient) SetBackground(ctx context.Context, jpeg []byte, expected uint64) (background.Info, error) {
	session, err := s.beginBackground(ctx)
	if err != nil {
		return background.Info{}, err
	}
	defer session.release()
	if len(jpeg) == 0 || len(jpeg) > background.MaxBytes {
		return background.Info{}, fmt.Errorf("背景图片必须为 1–192 KiB")
	}
	body := bytes.Clone(jpeg)
	if err := background.ValidateJPEG(body); err != nil {
		return background.Info{}, err
	}
	info, err := session.info(http.MethodPut, body, &expected)
	if err != nil {
		return info, err
	}
	sum := sha256.Sum256(body)
	if !info.Present || !info.Displayed || info.Error != "" || info.Bytes != len(body) || info.SHA256 != hex.EncodeToString(sum[:]) || info.Revision < expected {
		return background.Info{}, fmt.Errorf("设备未确认应用本次背景，请重新查询设备状态")
	}
	return info, nil
}

func (s *backgroundClient) ClearBackground(ctx context.Context, expected uint64) (background.Info, error) {
	session, err := s.beginBackground(ctx)
	if err != nil {
		return background.Info{}, err
	}
	defer session.release()
	info, err := session.info(http.MethodDelete, nil, &expected)
	if err != nil {
		return info, err
	}
	if info.Present || info.Displayed || info.Error != "" || info.Revision < expected {
		return background.Info{}, fmt.Errorf("设备未确认移除背景，请重新查询设备状态")
	}
	return info, nil
}
