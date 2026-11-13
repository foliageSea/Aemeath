package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"aemeath/agent/internal/background"
)

func TestBackgroundEmptyRecordWithDisplayErrorIsNotDeleteSuccess(t *testing.T) {
	info := backgroundInfo(nil, 8)
	info.Error = "display update failed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sendBackgroundInfo(w, info) }))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, nil)
	got, err := s.GetBackground(context.Background())
	if err != nil || got.Present || got.Error != info.Error {
		t.Fatalf("status: %+v %v", got, err)
	}
	if _, err := s.ClearBackground(context.Background(), 7); err == nil {
		t.Fatal("EMPTY persisted but display failure was reported as successful delete")
	}
}

func TestBackgroundMaximumJPEGWireBoundary(t *testing.T) {
	original := backgroundJPEG(t)
	padded := bytes.Clone(original[:2])
	remaining := background.MaxBytes - len(original)
	for remaining > 0 {
		n := min(remaining, 65537)
		if rest := remaining - n; rest > 0 && rest < 4 {
			n -= 4
		}
		segment := make([]byte, n)
		segment[0], segment[1], segment[2], segment[3] = 255, 226, byte((n-2)>>8), byte(n-2)
		padded = append(padded, segment...)
		remaining -= n
	}
	padded = append(padded, original[2:]...)
	if len(padded) != background.MaxBytes {
		t.Fatal(len(padded))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			body, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(body, padded) {
				t.Error("wire boundary body")
			}
			sendBackgroundInfo(w, backgroundInfo(padded, 1))
			return
		}
		if r.URL.Path == "/api/background/image" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(padded)
			return
		}
		sendBackgroundInfo(w, backgroundInfo(padded, 1))
	}))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, nil)
	if _, err := s.SetBackground(context.Background(), padded, 0); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBackgroundImage(context.Background())
	if err != nil || !bytes.Equal(got, padded) {
		t.Fatalf("max image %v", err)
	}
}

func TestBackgroundReadBoundsDeclaredLengthAndChunked(t *testing.T) {
	for _, declared := range []int64{-1, 5, backgroundJSONLimit + 1} {
		t.Run(strconv.FormatInt(declared, 10), func(t *testing.T) {
			response := &http.Response{StatusCode: 200, ContentLength: declared, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", backgroundJSONLimit+1)))}
			if _, err := boundedBackgroundBody(response, backgroundJSONLimit); err == nil {
				t.Fatal("invalid body length accepted")
			}
		})
	}
	short := &http.Response{ContentLength: 5, Body: io.NopCloser(strings.NewReader("{}"))}
	if _, err := boundedBackgroundBody(short, backgroundJSONLimit); err == nil {
		t.Fatal("short body accepted")
	}
	exact := &http.Response{ContentLength: -1, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", backgroundJSONLimit)))}
	if data, err := boundedBackgroundBody(exact, backgroundJSONLimit); err != nil || len(data) != backgroundJSONLimit {
		t.Fatal(err)
	}
}

func TestBackgroundClientDefaultDeadlineAndSynchronousCancellationSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sendBackgroundInfo(w, backgroundInfo(nil, 0)) }))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, &http.Client{Timeout: time.Hour})
	session, err := s.beginBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.client.Timeout != 30*time.Second {
		t.Fatal(session.client.Timeout)
	}
	deadline, ok := session.ctx.Deadline()
	if !ok || time.Until(deadline) > 30*time.Second {
		t.Fatal("missing total deadline")
	}
	session.release()
	// Check the source context itself, not only an asynchronously scheduled
	// AfterFunc, so a response cannot race through just after cancellation.
	connectionCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	oldCtx := s.connCtx
	s.connCtx = connectionCtx
	s.mu.Unlock()
	session, err = s.beginBackground(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.release()
	cancel()
	if !errors.Is(session.current(), context.Canceled) {
		t.Fatal("canceled source accepted")
	}
	s.mu.Lock()
	s.connCtx = oldCtx
	s.mu.Unlock()
}

func TestBackgroundRejectsWrongSuccessCodeAndInvalidUTF8(t *testing.T) {
	info := backgroundInfo(nil, 0)
	data, _ := json.Marshal(info)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "\"0\"")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	s := backgroundService(t, server, nil)
	if _, err := s.GetBackground(context.Background()); err == nil {
		t.Fatal("GET 201 accepted")
	}
	invalid := strings.Replace(string(data), "\"scope\":\"shared\"", "\"scope\":\"shared\",\"error\":\"X\"", 1)
	broken := []byte(strings.Replace(invalid, "X", string([]byte{255}), 1))
	if _, err := decodeBackgroundInfo(broken, "\"0\""); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}
