package wake

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func modelFixture(t *testing.T) (modelInstallSource, []byte, []byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var models []asset
	for in, out := range archiveFiles {
		b := []byte("fixture:" + out)
		if err := tw.WriteHeader(&tar.Header{Name: ModelVersion + "/" + in, Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
		models = append(models, asset{out, sum(b), int64(len(b))})
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw, vad := buf.Bytes(), []byte("fixture vad")
	return modelInstallSource{archive: asset{"archive", sum(raw), int64(len(raw))}, models: models, vad: asset{vadAsset.Name, sum(vad), int64(len(vad))}, decode: func(r io.Reader) io.Reader { return r }}, raw, vad
}

func serveModelFixture(t *testing.T, src *modelInstallSource, archive, vad []byte) *atomic.Int32 {
	t.Helper()
	requests := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/model":
			_, _ = w.Write(archive)
		case "/vad":
			_, _ = w.Write(vad)
		default:
			http.Error(w, "unexpected download", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	src.client, src.modelURL, src.vadURL = srv.Client(), srv.URL+"/model", srv.URL+"/vad"
	return requests
}

func assertNoModelStage(t *testing.T, parent string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(parent, ".wake-install-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staging remains: %v (%v)", matches, err)
	}
}

func TestInstallWakeModelsProgressAndIdempotence(t *testing.T) {
	src, raw, vad := modelFixture(t)
	requests := serveModelFixture(t, &src, raw, vad)
	parent := t.TempDir()
	dst := filepath.Join(parent, "exact-target")
	var progress []InstallProgress
	if err := installWakeModels(context.Background(), dst, func(p InstallProgress) { progress = append(progress, p) }, src); err != nil {
		t.Fatal(err)
	}
	wantStages := []string{"checking", "downloading-model", "extracting", "downloading-vad", "verifying", "complete"}
	var stages []string
	for i := 0; i < len(progress); {
		end := i + 1
		for end < len(progress) && progress[end].Stage == progress[i].Stage {
			end++
		}
		group := progress[i:end]
		stages = append(stages, group[0].Stage)
		if len(group) < 2 {
			t.Fatalf("missing stage boundaries: %+v", group)
		}
		total := int64(0)
		if group[0].Stage == "downloading-model" {
			total = src.archive.Size
		}
		if group[0].Stage == "downloading-vad" {
			total = src.vad.Size
		}
		if group[0].Downloaded != 0 || group[0].Total != total || group[len(group)-1].Downloaded != total {
			t.Fatalf("invalid boundaries: %+v", group)
		}
		var previous int64
		for _, p := range group {
			if p.Total != total || p.Downloaded < previous || p.Downloaded > total {
				t.Fatalf("invalid byte progress: %+v", group)
			}
			previous = p.Downloaded
		}
		i = end
	}
	if !reflect.DeepEqual(stages, wantStages) {
		t.Fatalf("stages %v", stages)
	}
	if requests.Load() != 2 {
		t.Fatalf("downloads %d", requests.Load())
	}
	if err := verifyWakeModels(context.Background(), dst, src.models, src.vad); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reference.wav", "reference-original.wav", "models"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected installed %s: %v", name, err)
		}
	}
	assertNoModelStage(t, parent)
	// A second install must succeed with a client that cannot make requests.
	src.client = nil
	progress = nil
	if err := installWakeModels(context.Background(), dst, func(p InstallProgress) { progress = append(progress, p) }, src); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 4 || progress[0].Stage != "checking" || progress[2].Stage != "complete" {
		t.Fatalf("idempotent progress: %+v", progress)
	}
}

func TestInstallWakeModelsCancellationAtBoundaries(t *testing.T) {
	for _, stage := range []string{"checking", "downloading-model", "extracting", "downloading-vad", "verifying"} {
		for _, atEnd := range []bool{false, true} {
			name := stage + "/start"
			if atEnd {
				name = stage + "/end"
			}
			t.Run(name, func(t *testing.T) {
				src, raw, vad := modelFixture(t)
				serveModelFixture(t, &src, raw, vad)
				parent := t.TempDir()
				dst := filepath.Join(parent, "models")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				count := 0
				complete := false
				err := installWakeModels(ctx, dst, func(p InstallProgress) {
					if p.Stage == "complete" {
						complete = true
					}
					if p.Stage == stage {
						count++
						end := count == 2
						if strings.HasPrefix(stage, "downloading-") {
							end = p.Downloaded == p.Total
						}
						if !atEnd || end {
							cancel()
						}
					}
				}, src)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error: %v", err)
				}
				if complete {
					t.Fatal("reported complete after cancellation")
				}
				if _, err := os.Lstat(dst); !os.IsNotExist(err) {
					t.Fatalf("published canceled installation: %v", err)
				}
				assertNoModelStage(t, parent)
			})
		}
	}
}

func TestInstallWakeModelsCancelInFlightDownload(t *testing.T) {
	src, _, _ := modelFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	src.client, src.modelURL = srv.Client(), srv.URL
	parent := t.TempDir()
	dst := filepath.Join(parent, "models")
	err := installWakeModels(ctx, dst, nil, src)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("request not canceled promptly")
	}
	assertNoModelStage(t, parent)
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("published partial models: %v", err)
	}
}

func TestInstallWakeModelsRejectsDownloadOrModelCorruption(t *testing.T) {
	for _, kind := range []string{"archive-hash", "archive-size", "model-hash", "vad-hash", "vad-size"} {
		t.Run(kind, func(t *testing.T) {
			src, raw, vad := modelFixture(t)
			switch kind {
			case "archive-hash":
				src.archive.SHA256 = strings.Repeat("0", 64)
			case "archive-size":
				src.archive.Size++
			case "model-hash":
				src.models[0].SHA256 = strings.Repeat("0", 64)
			case "vad-hash":
				src.vad.SHA256 = strings.Repeat("0", 64)
			case "vad-size":
				src.vad.Size--
			}
			serveModelFixture(t, &src, raw, vad)
			parent := t.TempDir()
			dst := filepath.Join(parent, "models")
			complete := false
			if err := installWakeModels(context.Background(), dst, func(p InstallProgress) { complete = complete || p.Stage == "complete" }, src); err == nil {
				t.Fatal("accepted corrupt download")
			}
			if complete {
				t.Fatal("corruption reported complete")
			}
			assertNoModelStage(t, parent)
			if _, err := os.Stat(dst); !os.IsNotExist(err) {
				t.Fatalf("published corrupt models: %v", err)
			}
		})
	}
}

func TestInstallWakeModelsPreservesExistingDestination(t *testing.T) {
	for _, kind := range []string{"empty", "custom", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "models")
			switch kind {
			case "empty", "custom":
				if err := os.Mkdir(dst, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "custom" {
					if err := os.WriteFile(filepath.Join(dst, "encoder.onnx"), []byte("custom model"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "file":
				if err := os.WriteFile(dst, []byte("custom"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(parent, "missing"), dst); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(dst)
			if err != nil {
				t.Fatal(err)
			}
			// Exported API must refuse before opening any network connection.
			if err := InstallWakeModels(context.Background(), dst, nil); err == nil {
				t.Fatal("accepted existing custom path")
			}
			after, err := os.Lstat(dst)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing destination changed: %v", err)
			}
			if kind == "custom" {
				b, err := os.ReadFile(filepath.Join(dst, "encoder.onnx"))
				if err != nil || string(b) != "custom model" {
					t.Fatalf("custom model changed: %q %v", b, err)
				}
			}
			assertNoModelStage(t, parent)
		})
	}
}

func TestVerifyWakeModelsRequiresEveryFile(t *testing.T) {
	src, raw, vad := modelFixture(t)
	serveModelFixture(t, &src, raw, vad)
	dst := filepath.Join(t.TempDir(), "models")
	if err := installWakeModels(context.Background(), dst, nil, src); err != nil {
		t.Fatal(err)
	}
	assets := append(append([]asset{}, src.models...), asset{"keywords.txt", sum([]byte(KeywordTokens)), int64(len(KeywordTokens))}, src.vad)
	for _, a := range assets {
		t.Run(a.Name, func(t *testing.T) {
			path := filepath.Join(dst, a.Name)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Error(err)
				}
			}()
			// Same-size corruption must be rejected as well as missing files.
			broken := bytes.Repeat([]byte("!"), len(original))
			if err := os.WriteFile(path, broken, 0600); err != nil {
				t.Fatal(err)
			}
			if err := verifyWakeModels(context.Background(), dst, src.models, src.vad); err == nil {
				t.Fatal("accepted wrong hash")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := verifyWakeModels(context.Background(), dst, src.models, src.vad); err == nil {
				t.Fatal("accepted missing file")
			}
		})
	}
	if err := verifyWakeModels(context.Background(), dst, src.models, src.vad); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWakeModels(dst); err == nil {
		t.Fatal("exported verifier did not enforce production pins")
	}
	if err := VerifyWakeModels(filepath.Join(dst, "missing")); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestInstallWakeModelsDestinationAppearsBeforeCommit(t *testing.T) {
	src, raw, vad := modelFixture(t)
	serveModelFixture(t, &src, raw, vad)
	parent := t.TempDir()
	dst := filepath.Join(parent, "models")
	created := false
	err := installWakeModels(context.Background(), dst, func(p InstallProgress) {
		if p.Stage == "verifying" && !created {
			if err := os.Mkdir(dst, 0700); err != nil {
				t.Fatal(err)
			}
			created = true
		}
	}, src)
	if err == nil {
		t.Fatal("overwrote newly created empty directory")
	}
	entries, err := os.ReadDir(dst)
	if err != nil || len(entries) != 0 {
		t.Fatalf("changed custom directory: %v %v", entries, err)
	}
	assertNoModelStage(t, parent)
}

func TestInstallWakeModelsDefaultDirectory(t *testing.T) {
	// Both supported Unix cache conventions are redirected before resolving defaults.
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("LocalAppData", root)
	dst := DefaultModelDir()
	relative, err := filepath.Rel(root, dst)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("default escaped temporary cache: %s (%v)", dst, err)
	}
	src, raw, vad := modelFixture(t)
	serveModelFixture(t, &src, raw, vad)
	if err := installWakeModels(context.Background(), "", nil, src); err != nil {
		t.Fatal(err)
	}
	if err := verifyWakeModels(context.Background(), dst, src.models, src.vad); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWakeModels(""); err == nil {
		t.Fatal("default verifier did not enforce production pins")
	}
	assertNoModelStage(t, filepath.Dir(dst))
}

func TestInstallProgressThrottlesIntermediateUpdates(t *testing.T) {
	var updates []InstallProgress
	p := &installReporter{callback: func(p InstallProgress) { updates = append(updates, p) }, stage: "downloading-model"}
	p.report(0, 1000, true)
	for i := int64(1); i < 1000; i++ {
		p.report(i, 1000, false)
	}
	p.report(1000, 1000, true)
	if len(updates) != 2 {
		t.Fatalf("intermediate reports were not throttled: %d", len(updates))
	}
	if updates[1].Downloaded != 1000 {
		t.Fatal("missing final byte count")
	}
}
