package wake

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTar(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range headers {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := io.CopyN(w, strings.NewReader(strings.Repeat("x", int(h.Size))), h.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestExtractionRejectsUnsafeEntries(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg}, {Name: "/absolute", Typeflag: tar.TypeReg}, {Name: ModelVersion + "/../escape", Typeflag: tar.TypeReg},
		{Name: ModelVersion + "/link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/escape"}, {Name: ModelVersion + "/hard", Typeflag: tar.TypeLink, Linkname: "x"},
		{Name: "C:/escape", Typeflag: tar.TypeReg}, {Name: "root\\escape", Typeflag: tar.TypeReg},
	} {
		t.Run(h.Name, func(t *testing.T) {
			if err := extractModels(bytes.NewReader(makeTar(t, []*tar.Header{h})), t.TempDir()); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}
func TestExtractionWhitelistAndDuplicates(t *testing.T) {
	var hs []*tar.Header
	for in := range archiveFiles {
		hs = append(hs, &tar.Header{Name: ModelVersion + "/" + in, Typeflag: tar.TypeReg, Size: 1})
	}
	hs = append(hs, &tar.Header{Name: ModelVersion + "/unrelated.txt", Typeflag: tar.TypeReg, Size: 1})
	dir := t.TempDir()
	if err := extractModels(bytes.NewReader(makeTar(t, hs)), dir); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 4 {
		t.Fatalf("unexpected extracted files %d", len(files))
	}
	hs = append(hs, hs[0])
	if err := extractModels(bytes.NewReader(makeTar(t, hs)), t.TempDir()); err == nil {
		t.Fatal("accepted duplicate")
	}
}
func TestExtractionBounds(t *testing.T) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	_ = w.WriteHeader(&tar.Header{Name: ModelVersion + "/huge", Size: 33 << 20, Typeflag: tar.TypeReg})
	if err := extractModels(bytes.NewReader(b.Bytes()), t.TempDir()); err == nil {
		t.Fatal("accepted oversized entry")
	}
	hs := make([]*tar.Header, 513)
	for i := range hs {
		hs[i] = &tar.Header{Name: fmt.Sprintf("%s/dir%d", ModelVersion, i), Typeflag: tar.TypeDir}
	}
	if err := extractModels(bytes.NewReader(makeTar(t, hs)), t.TempDir()); err == nil {
		t.Fatal("accepted too many entries")
	}
}
func TestDownloadHashSizeAndCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("payload")) }))
	defer srv.Close()
	for _, tc := range []struct {
		name    string
		a       asset
		wantErr bool
	}{{"ok", asset{"x", sum([]byte("payload")), 7}, false}, {"hash", asset{"x", strings.Repeat("0", 64), 7}, true}, {"oversize", asset{"x", sum([]byte("pay")), 3}, true}, {"short", asset{"x", sum([]byte("payload")), 8}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			err := download(context.Background(), srv.Client(), srv.URL, filepath.Join(t.TempDir(), "a"), tc.a)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := download(ctx, srv.Client(), srv.URL, filepath.Join(t.TempDir(), "a"), asset{"x", sum([]byte("payload")), 7}); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestCorruptInstallIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reference.wav"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallAssets(context.Background(), dir, true); err == nil {
		t.Fatal("accepted corruption")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "reference.wav"))
	if string(b) != "broken" {
		t.Fatal("overwrote existing reference")
	}
	if err := VerifyAssets(t.TempDir(), false); err == nil {
		t.Fatal("missing assets accepted")
	}
}
