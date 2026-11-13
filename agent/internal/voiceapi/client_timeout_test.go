package voiceapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/netproxy"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testResponse(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, ContentLength: -1}
}

func TestMiMoOperationDeadlinesAndTransportBounds(t *testing.T) {
	c := New("https://example.invalid/v1", "synthetic-key")
	if c.http.Timeout != 0 {
		t.Fatalf("whole-client timeout can truncate batch TTS: %s", c.http.Timeout)
	}
	if c.http.Transport == nil || transportTemplate.DialContext == nil || transportTemplate.TLSHandshakeTimeout != 10*time.Second || transportTemplate.ResponseHeaderTimeout != TTSRequestTimeout {
		t.Fatal("missing bounded connection/header transport")
	}
	if ASRRequestTimeout != 60*time.Second || TTSRequestTimeout != 180*time.Second || c.bodyTimeout != TTSRequestTimeout {
		t.Fatal("unexpected operation/response-body budget")
	}
	var calls int
	c.http.Transport = testRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		budget := ASRRequestTimeout
		if calls == 2 {
			budget = TTSRequestTimeout
		}
		deadline, ok := r.Context().Deadline()
		if remaining := time.Until(deadline); !ok || remaining <= budget-time.Second || remaining > budget {
			t.Errorf("call %d budget=%s remaining=%s deadline set=%v", calls, budget, remaining, ok)
		}
		if r.GetBody != nil {
			t.Error("paid POST must not be implicitly replayable")
		}
		wav := base64.StdEncoding.EncodeToString(EncodeWAV([]int16{1, 2}, 16000))
		return testResponse(io.NopCloser(strings.NewReader(fmt.Sprintf(

			dialectResponse, wav)))), nil
	})
	if _, err := c.Recognize(context.Background(), []int16{1}); err != nil {
		t.Fatal(err)
	}
	ref := writeReference(t, EncodeWAV([]int16{1, 2}, 16000))
	if _, err := c.Synthesize(context.Background(), "test", ref); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("requests=%d", calls)
	}
}

const dialectResponse = `{"choices":[{"message":{"content":"ok","audio":{"data":"%s"}}}]}`

func TestSlowCloneBodyHasItsOwnBoundedBudget(t *testing.T) {
	var calls atomic.Int32
	ref := writeReference(t, EncodeWAV([]int16{1}, 16000))
	wav := base64.StdEncoding.EncodeToString(EncodeWAV([]int16{12, 34}, 16000))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		// Time waiting for generation headers must not consume a separate body cap.
		time.Sleep(60 * time.Millisecond)
		io.WriteString(w, `{"choices":[{"message":{"audio":{"data":"`)
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprintf(w, `%s"}}}]}`, wav)
	}))
	defer s.Close()
	c := New(s.URL, "synthetic-key")
	c.ttsTimeout = time.Second
	c.bodyTimeout = 100 * time.Millisecond
	pcm, err := c.Synthesize(context.Background(), "hello", ref)
	if err != nil || len(pcm) != 2 || pcm[0] != 12 || pcm[1] != 34 || calls.Load() != 1 {
		t.Fatalf("PCM=%v err=%v calls=%d", pcm, err, calls.Load())
	}
}

func TestBodyDeadlineAndCallerCancellationDoNotReplay(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller_cancel=%v", cancelCaller), func(t *testing.T) {
			var calls atomic.Int32
			entered := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				io.WriteString(w, "synthetic-secret-private-response")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer s.Close()
			c := New(s.URL, "synthetic-secret")
			transport := c.http.Transport
			c.http.Transport = testRoundTripper(func(r *http.Request) (*http.Response, error) {
				res, err := transport.RoundTrip(r)
				if err == nil {
					res.Body = &testReadSignaler{ReadCloser: res.Body, entered: entered}
				}
				return res, err
			})
			c.asrTimeout = time.Second
			c.bodyTimeout = 40 * time.Millisecond
			if cancelCaller {
				c.bodyTimeout = time.Second
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.Recognize(ctx, []int16{1}); done <- err }()
			<-entered
			want := context.DeadlineExceeded
			if cancelCaller {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) || !strings.Contains(err.Error(), "response body") || !strings.Contains(err.Error(), "HTTP 200") || strings.Contains(err.Error(), "synthetic-secret") || calls.Load() != 1 {
					t.Fatalf("err=%v calls=%d", err, calls.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("response-body cancellation stalled")
			}
		})
	}
}

type testReadSignaler struct {
	io.ReadCloser
	entered  chan struct{}
	signaled atomic.Bool
}

func (b *testReadSignaler) Read(p []byte) (int, error) {
	if b.signaled.CompareAndSwap(false, true) {
		close(b.entered)
	}
	return b.ReadCloser.Read(p)
}

type testFailingBody struct {
	io.Reader
	err error
}

func (b *testFailingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}
func (*testFailingBody) Close() error { return nil }

type testPrivateTimeout struct{}

func (testPrivateTimeout) Error() string {
	return "synthetic-secret https://private.invalid/path?token=private"
}
func (testPrivateTimeout) Timeout() bool   { return true }
func (testPrivateTimeout) Temporary() bool { return true }

func TestBodyFailuresAreSanitizedAndNeverReplayed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		err    error
		want   error
		label  string
	}{
		{"unexpected_eof", 200, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, "truncated response body"},
		{"rejection_with_truncated_body", 503, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, "truncated response body"},
		{"private_timeout", 200, testPrivateTimeout{}, context.DeadlineExceeded, "response body timed out"},
		{"private_read_error", 200, errors.New("synthetic-secret private response"), nil, "connection interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			c := New("https://example.invalid", "synthetic-secret")
			c.http.Transport = testRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				res := testResponse(&testFailingBody{Reader: strings.NewReader("synthetic-secret private partial body"), err: tc.err})
				res.StatusCode = tc.status
				return res, nil
			})
			_, err := c.Recognize(context.Background(), []int16{1})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || !strings.Contains(err.Error(), tc.label) || !strings.Contains(err.Error(), "received 37 bytes") || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "private.invalid") || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestTruncatedContentLengthIsRejectedWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", "1000")
		io.WriteString(w, `{"choices":[{"message":{"content":"must not accept partial transfer"}}]}`)
	}))
	defer s.Close()
	_, err := New(s.URL, "synthetic-key").Recognize(context.Background(), []int16{1})
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "truncated response body") || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestAmbiguousTransportFailuresNeverReplayClone(t *testing.T) {
	ref := writeReference(t, EncodeWAV([]int16{1}, 16000))
	for _, failure := range []error{io.EOF, io.ErrUnexpectedEOF, testPrivateTimeout{}, errors.New("synthetic-secret private request")} {
		var calls int
		c := New("https://example.invalid", "synthetic-secret")
		c.http.Transport = testRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.GetBody != nil {
				t.Error("replayable paid request")
			}
			return nil, failure
		})
		_, err := c.Synthesize(context.Background(), "private text", ref)
		if err == nil || !strings.Contains(err.Error(), "MiMo TTS") || !strings.Contains(err.Error(), "before response headers") || strings.Contains(err.Error(), "synthetic-secret") || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}

type testCloseUnblocksBody struct {
	closed chan struct{}
	closes atomic.Int32
}

func (b *testCloseUnblocksBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, errors.New("synthetic-secret custom body closed")
}
func (b *testCloseUnblocksBody) Close() error {
	b.closes.Add(1)
	close(b.closed)
	return nil
}

func TestResponsePhaseTimeoutClosesCustomBodyExactlyOnce(t *testing.T) {
	body := &testCloseUnblocksBody{closed: make(chan struct{})}
	c := New("https://example.invalid", "synthetic-secret")
	c.bodyTimeout = 20 * time.Millisecond
	c.http.Transport = testRoundTripper(func(*http.Request) (*http.Response, error) { return testResponse(body), nil })
	start := time.Now()
	_, err := c.Recognize(context.Background(), []int16{1})
	if !errors.Is(err, context.DeadlineExceeded) || body.closes.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("err=%v closes=%d elapsed=%s", err, body.closes.Load(), time.Since(start))
	}
}

func TestHeaderTimeoutNeverReplays(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer s.Close()
	c := New(s.URL, "synthetic-key")
	c.asrTimeout = time.Second
	transport := transportTemplate.Clone()
	transport.ResponseHeaderTimeout = 30 * time.Millisecond
	c.http.Transport = netproxy.Transport(transport)
	start := time.Now()
	_, err := c.Recognize(context.Background(), []int16{1})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "before response headers") || calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("err=%v calls=%d elapsed=%s", err, calls.Load(), time.Since(start))
	}
}

func TestOperationDeadlineIncludesSafeRetryWait(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(503)
	}))
	defer s.Close()
	c := New(s.URL, "synthetic-key")
	c.asrTimeout = 40 * time.Millisecond
	start := time.Now()
	_, err := c.Recognize(context.Background(), []int16{1})
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("err=%v calls=%d elapsed=%s", err, calls.Load(), time.Since(start))
	}
}
