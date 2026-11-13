//go:build darwin

package netproxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestReadNativeSnapshot(t *testing.T) {
	// Read only: no network access and no changes to the host's proxy settings.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := readNative(ctx); err != nil {
		t.Fatalf("scutil native snapshot: %v", err)
	}
}

func TestNativeOutputLimitAndCancellation(t *testing.T) {
	w := &limitedOutput{}
	if _, err := w.Write(bytes.Repeat([]byte("x"), 64*1024)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("unbounded output: %v", err)
	}
	w = &limitedOutput{}
	_, err := io.Copy(w, struct{ io.Reader }{bytes.NewReader(bytes.Repeat([]byte("x"), 64*1024+1))})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("io.Copy bypassed output limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readNative(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("native cancel: %v", err)
	}
}
