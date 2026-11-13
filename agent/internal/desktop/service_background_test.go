package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/background"
)

func backgroundService(t *testing.T, server *httptest.Server, client *http.Client) *Service {
	t.Helper()
	deps := testDeps()
	deps.find = func(context.Context) (string, error) { return strings.TrimPrefix(server.URL, "http://"), nil }
	deps.backgroundHTTP = client
	s := testService(t, deps)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Snapshot().Connected })
	return s
}
func backgroundJPEG(t *testing.T) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, background.Size, background.Size))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = 35, 90, 180, 255
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func backgroundInfo(data []byte, revision uint64) background.Info {
	info := background.Info{V: 1, Supported: true, Revision: revision, MaxBytes: background.MaxBytes, Scope: "shared"}
	if len(data) > 0 {
		sum := sha256.Sum256(data)
		info.Present, info.Displayed = true, true
		info.SHA256 = hex.EncodeToString(sum[:])
		info.Width, info.Height = background.Size, background.Size
		info.Bytes = len(data)
	}
	return info
}
func sendBackgroundInfo(w http.ResponseWriter, info background.Info) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(info.Revision, 10)))
	_ = json.NewEncoder(w).Encode(info)
}

func TestBackgroundServiceRoundTripProtocolAndActualTarget(t *testing.T) {
	data := backgroundJPEG(t)
	var mu sync.Mutex
	stored := []byte(nil)
	revision := uint64(0)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/background/image" {
			if r.Method != "GET" || r.Header.Get("If-Match") != strconv.Quote(strconv.FormatUint(revision, 10)) {
				t.Error("image revision header")
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(stored)
			return
		}
		if r.URL.Path != "/api/background" {
			t.Errorf("unexpected URL %s", r.URL)
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "GET":
			if r.Header.Get("If-Match") != "" {
				t.Error("unexpected state If-Match")
			}
		case "PUT":
			if r.Header.Get("If-Match") != strconv.Quote(strconv.FormatUint(revision, 10)) || r.Header.Get("Content-Type") != "image/jpeg" || r.ContentLength != int64(len(data)) {
				t.Error("upload protocol headers")
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			sum := sha256.Sum256(body)
			if !bytes.Equal(body, data) || r.Header.Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) {
				t.Error("upload digest/body")
			}
			stored = bytes.Clone(body)
			revision++
		case "DELETE":
			if r.Header.Get("If-Match") != strconv.Quote(strconv.FormatUint(revision, 10)) {
				t.Error("delete revision header")
			}
			stored = nil
			revision++
		default:
			t.Errorf("method %s", r.Method)
		}
		sendBackgroundInfo(w, backgroundInfo(stored, revision))
	}))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, nil)
	// An unsaved/changed preference cannot become an HTTP target.
	s.mu.Lock()
	s.state.Settings.Address = "not-the-connected-device.invalid:1"
	s.mu.Unlock()
	info, err := s.GetBackground(context.Background())
	if err != nil || info.Present || info.Revision != 0 {
		t.Fatalf("initial %+v %v", info, err)
	}
	info, err = s.SetBackground(context.Background(), data, 0)
	if err != nil || !info.Displayed || info.Revision != 1 {
		t.Fatalf("set %+v %v", info, err)
	}
	imageData, err := s.GetBackgroundImage(context.Background())
	if err != nil || !bytes.Equal(imageData, data) {
		t.Fatalf("get image %v", err)
	}
	imageData[0] = 0 // returned memory is caller-owned
	info, err = s.ClearBackground(context.Background(), 1)
	if err != nil || info.Present || info.Displayed || info.Revision != 2 {
		t.Fatalf("clear %+v %v", info, err)
	}
	if _, err := s.GetBackgroundImage(context.Background()); !errors.Is(err, ErrBackgroundNoImage) {
		t.Fatal(err)
	}
	if calls.Load() != 6 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if !s.Snapshot().Connected {
		t.Fatal("background changed metrics connection")
	}
}

func TestBackgroundOldFirmwareStatusAndNoRedirect(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		sendBackgroundInfo(w, backgroundInfo(nil, 0))
	}))
	t.Cleanup(destination.Close)
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }},
		{"not-found", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }},
		{"html", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<!doctype html><h1>WiFi</h1>")
		}},
		{"html-mislabeled", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "<html>WiFi</html>")
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			t.Cleanup(server.Close)
			s := backgroundService(t, server, &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }})
			_, err := s.GetBackground(context.Background())
			if err == nil {
				t.Fatal("legacy response accepted")
			}
			if !errors.Is(err, ErrBackgroundUnsupported) {
				t.Fatal(err)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("followed legacy redirect")
	}
}

func TestBackgroundHTTPErrorCodesNeverRetry(t *testing.T) {
	data := backgroundJPEG(t)
	for _, status := range []int{409, 412, 413, 415, 422, 503, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "private device diagnostic")
			}))
			t.Cleanup(server.Close)
			s := backgroundService(t, server, nil)
			_, err := s.SetBackground(context.Background(), data, 3)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
			if status == 409 && !errors.Is(err, ErrBackgroundBusy) || status == 412 && !errors.Is(err, ErrBackgroundConflict) {
				t.Fatal(err)
			}
			if requests.Load() != 1 || !s.Snapshot().Connected {
				t.Fatalf("retry or disconnected: %d", requests.Load())
			}
		})
	}
}

func TestBackgroundStrictStatusShapesAndETag(t *testing.T) {
	good := backgroundInfo(nil, 0)
	base, _ := json.Marshal(good)
	cases := map[string]struct {
		body              string
		etag, contentType string
	}{
		"missing-fields": {body: "{}", etag: "0", contentType: "application/json"},
		"wrong-tag":      {body: string(base), etag: "W/\"0\"", contentType: "application/json"},
		"unquoted-tag":   {body: string(base), etag: "0", contentType: "application/json"},
		"missing-tag":    {body: string(base), contentType: "application/json"},
		"wrong-type":     {body: string(base), etag: "\"0\"", contentType: "text/plain"},
		"oversized":      {body: strings.Repeat(" ", backgroundJSONLimit+1), etag: "\"0\"", contentType: "application/json"},
		"array":          {body: "[]", etag: "\"0\"", contentType: "application/json"},
		"null":           {body: "null", etag: "\"0\"", contentType: "application/json"},
		"trailing":       {body: string(base) + "{}", etag: "\"0\"", contentType: "application/json"},
		"duplicate":      {body: strings.Replace(string(base), "\"v\":1", "\"v\":1,\"v\":1", 1), etag: "\"0\"", contentType: "application/json"},
	}
	for _, key := range []string{"v", "supported", "revision", "maxBytes", "scope", "sha256"} {
		var values map[string]any
		_ = json.Unmarshal(base, &values)
		values[key] = nil
		b, _ := json.Marshal(values)
		cases["null-"+key] = struct {
			body              string
			etag, contentType string
		}{string(b), "\"0\"", "application/json"}
	}
	for _, change := range []struct {
		name  string
		apply func(*background.Info)
	}{
		{"version", func(i *background.Info) { i.V = 2 }}, {"max", func(i *background.Info) { i.MaxBytes-- }}, {"scope", func(i *background.Info) { i.Scope = "metrics" }},
		{"empty-displayed", func(i *background.Info) { i.Displayed = true }}, {"empty-dimensions", func(i *background.Info) { i.Width = 480 }}, {"empty-hash", func(i *background.Info) { i.SHA256 = strings.Repeat("0", 64) }},
		{"present-missing-hash", func(i *background.Info) { i.Present = true; i.Revision = 1; i.Width, i.Height = 480, 480; i.Bytes = 1 }},
	} {
		i := good
		change.apply(&i)
		b, _ := json.Marshal(i)
		cases[change.name] = struct {
			body              string
			etag, contentType string
		}{string(b), strconv.Quote(strconv.FormatUint(i.Revision, 10)), "application/json"}
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				if tt.etag != "" {
					w.Header().Set("ETag", tt.etag)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			s := backgroundService(t, server, nil)
			if _, err := s.GetBackground(context.Background()); err == nil {
				t.Fatalf("accepted %s", tt.body)
			}
		})
	}
	unsupported := backgroundInfo(nil, 0)
	unsupported.Supported = false
	unsupported.Error = "background partition missing"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sendBackgroundInfo(w, unsupported) }))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, nil)
	info, err := s.GetBackground(context.Background())
	if !errors.Is(err, ErrBackgroundUnsupported) || info.Supported || info.Error != unsupported.Error {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestBackgroundImageBoundedValidatedAndMatchedToStatus(t *testing.T) {
	data := backgroundJPEG(t)
	cases := []struct {
		name   string
		body   []byte
		kind   string
		status int
		want   error
	}{
		{"hash", append(bytes.Clone(data[:len(data)-2]), 255, 255, 217), "image/jpeg", 200, ErrBackgroundConflict},
		{"large", bytes.Repeat([]byte{0}, background.MaxBytes+1), "image/jpeg", 200, nil},
		{"html", []byte("<html>settings</html>"), "text/html", 200, nil},
		{"truncated", data[:len(data)-2], "image/jpeg", 200, nil},
		{"wrong-size", func() []byte {
			var b bytes.Buffer
			_ = jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
			return b.Bytes()
		}(), "image/jpeg", 200, nil},
		{"missing", nil, "application/json", 404, ErrBackgroundNoImage},
		{"changed", nil, "application/json", 412, ErrBackgroundConflict},
	}
	// A different but valid JPEG exercises hash mismatch rather than format errors.
	other := image.NewRGBA(image.Rect(0, 0, 480, 480))
	var b bytes.Buffer
	_ = jpeg.Encode(&b, other, nil)
	cases[0].body = b.Bytes()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/background" {
					sendBackgroundInfo(w, backgroundInfo(data, 3))
					return
				}
				w.Header().Set("Content-Type", tt.kind)
				w.WriteHeader(tt.status)
				_, _ = w.Write(tt.body)
			}))
			t.Cleanup(server.Close)
			s := backgroundService(t, server, nil)
			_, err := s.GetBackgroundImage(context.Background())
			if err == nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestBackgroundWritesRejectInvalidInputAndWrongAcknowledgement(t *testing.T) {
	valid := backgroundJPEG(t)
	var count atomic.Int32
	var infoMu sync.Mutex
	info := backgroundInfo(nil, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		infoMu.Lock()
		defer infoMu.Unlock()
		sendBackgroundInfo(w, info)
	}))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, nil)
	for _, bad := range [][]byte{nil, bytes.Repeat([]byte{1}, background.MaxBytes+1), []byte("png"), valid[:len(valid)-1]} {
		if _, err := s.SetBackground(context.Background(), bad, 3); err == nil {
			t.Fatal("bad upload accepted")
		}
	}
	if count.Load() != 0 {
		t.Fatal("invalid input reached device")
	}
	if _, err := s.SetBackground(context.Background(), valid, 3); err == nil {
		t.Fatal("empty acknowledgement accepted")
	}
	infoMu.Lock()
	info = backgroundInfo(valid, 4)
	infoMu.Unlock()
	if _, err := s.ClearBackground(context.Background(), 3); err == nil {
		t.Fatal("present delete acknowledgement accepted")
	}
	infoMu.Lock()
	info = backgroundInfo(valid, 2)
	infoMu.Unlock()
	if _, err := s.SetBackground(context.Background(), valid, 3); err == nil {
		t.Fatal("older revision accepted")
	}
}

func TestBackgroundCancellationStopSwitchCloseAndNoSendLock(t *testing.T) {
	for _, action := range []string{"caller", "stop", "switch", "close"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
			t.Cleanup(server.Close)
			s := backgroundService(t, server, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := s.GetBackground(ctx); result <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request not started")
			}
			// Screen/page sends must not wait for a stalled HTTP transfer.
			page := make(chan error, 1)
			go func() { page <- s.SetPage("quota") }()
			select {
			case err := <-page:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("background holds sendMu")
			}
			switch action {
			case "caller":
				cancel()
			case "stop":
				if err := s.Stop(); err != nil {
					t.Fatal(err)
				}
			case "switch":
				settings := s.Snapshot().Settings
				settings.Address = "other-device.invalid:80"
				if err := s.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
			case "close":
				s.Close()
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled request accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not stop request")
			}
		})
	}
}

func TestBackgroundWriteTimeoutIsAmbiguousAndNeverRetried(t *testing.T) {
	data := backgroundJPEG(t)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 25 * time.Millisecond}
	s := backgroundService(t, server, client)
	_, err := s.SetBackground(context.Background(), data, 0)
	if err == nil {
		t.Fatal("timeout accepted")
	}
	if strings.Contains(err.Error(), "保留") || attempts.Load() != 1 || client.CheckRedirect != nil || client.Timeout != 25*time.Millisecond {
		t.Fatalf("wrong timeout/retry/client mutation: %v (%d)", err, attempts.Load())
	}
}

func TestBackgroundOfflineClosedAndConcurrentCloseAdmission(t *testing.T) {
	s := testService(t, testDeps())
	if _, err := s.GetBackground(context.Background()); !errors.Is(err, ErrOffline) {
		t.Fatal(err)
	}
	if _, err := s.GetBackground(nil); err == nil {
		t.Fatal("nil context accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GetBackground(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.Close()
	if _, err := s.GetBackground(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sendBackgroundInfo(w, backgroundInfo(nil, 0)) }))
	t.Cleanup(server.Close)
	active := backgroundService(t, server, nil)
	var wg sync.WaitGroup
	for n := 0; n < 30; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 5; k++ {
				_, _ = active.GetBackground(context.Background())
			}
		}()
	}
	active.Close()
	wg.Wait()
	if active.Snapshot().Status != "closed" {
		t.Fatal("close not completed")
	}
}

func TestBackgroundRejectsStaleSuccessEvenWhenTransportIgnoresCancel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: backgroundRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		data, _ := json.Marshal(backgroundInfo(nil, 0))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}, "Etag": []string{"\"0\""}}, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}, nil
	})}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, client)
	result := make(chan error, 1)
	go func() { _, err := s.GetBackground(context.Background()); result <- err }()
	<-started
	s.mu.Lock()
	s.conn = &fakeConn{}
	s.state.Target = "new-device:80"
	s.mu.Unlock()
	close(release)
	if err := <-result; !errors.Is(err, ErrOffline) {
		t.Fatalf("stale success: %v", err)
	}
}

type backgroundRoundTripper func(*http.Request) (*http.Response, error)

func (f backgroundRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBackgroundInfoRevisionUint64AndJSONUnknownFields(t *testing.T) {
	info := backgroundInfo(nil, ^uint64(0))
	data, _ := json.Marshal(info)
	if got, err := decodeBackgroundInfo(data, strconv.Quote(strconv.FormatUint(info.Revision, 10))); err != nil || got.Revision != info.Revision {
		t.Fatalf("uint64 revision: %v", err)
	}
	for _, data := range [][]byte{[]byte(strings.Replace(string(data), "\"v\":1", "\"v\":1,\"unexpected\":true", 1)), []byte(strings.Replace(string(data), "18446744073709551615", "18446744073709551616", 1))} {
		if _, err := decodeBackgroundInfo(data, "\"0\""); err == nil {
			t.Fatal("unknown/overflow accepted")
		}
	}
}
