package wake

import (
	"compress/bzip2"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"aemeath/agent/internal/netproxy"
)

// InstallProgress describes synchronous installation progress. Stage is checking,
// downloading-model, extracting, downloading-vad, verifying, or complete.
// Downloaded and Total count bytes of the current download (archive or VAD), not
// cumulative bytes. Other stages use zero counts. Every entered stage reports
// its start and end, including failure; complete is emitted only on success.
// Intermediate download reports are limited to one per 100ms; boundary reports
// are unconditional. Callbacks run on the caller's goroutine and may cancel ctx.
type InstallProgress struct {
	Stage             string
	Downloaded, Total int64
}

// VerifyWakeModels checks the four pinned model files, exact keywords and VAD
// locally, without accessing the network or requiring reference audio. modelDir
// is the exact model directory; an empty value selects DefaultModelDir().
func VerifyWakeModels(modelDir string) error {
	if modelDir == "" {
		modelDir = DefaultModelDir()
	}
	return verifyWakeModels(context.Background(), modelDir, modelAssets, vadAsset)
}

func verifyWakeModels(ctx context.Context, dir string, models []asset, vad asset) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("model path is not a directory: %s", dir)
	}
	assets := append(append([]asset{}, models...), asset{"keywords.txt", sum([]byte(KeywordTokens)), int64(len(KeywordTokens))}, vad)
	for _, a := range assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verifyFileContext(ctx, filepath.Join(dir, a.Name), a); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// InstallWakeModels installs only wake models, keywords and VAD, never reference
// audio. modelDir is the exact destination, defaulting to DefaultModelDir().
// Verified installations need no network. Any other existing destination is
// rejected, including empty directories and symlinks. Files are verified in a
// private staging directory on the destination filesystem, then atomically
// renamed into place. Cancellation/failure removes staging and never publishes
// a partial model directory. A nil progress callback is allowed.
func InstallWakeModels(ctx context.Context, modelDir string, progress func(InstallProgress)) error {
	return installWakeModels(ctx, modelDir, progress, modelInstallSource{
		client: &http.Client{Transport: netproxy.Transport(nil), Timeout: 3 * time.Minute}, modelURL: modelURL, vadURL: vadURL,
		archive: asset{"archive", archiveSHA, 32654866}, models: modelAssets, vad: vadAsset,
		decode: func(r io.Reader) io.Reader { return bzip2.NewReader(r) },
	})
}

// modelInstallSource permits small local HTTP fixtures without changing pinned
// production assets, the real cache, or process-global HTTP state.
type modelInstallSource struct {
	client           *http.Client
	modelURL, vadURL string
	archive          asset
	models           []asset
	vad              asset
	decode           func(io.Reader) io.Reader
}

func installWakeModels(ctx context.Context, modelDir string, callback func(InstallProgress), src modelInstallSource) error {
	if modelDir == "" {
		modelDir = DefaultModelDir()
	}
	modelDir = filepath.Clean(modelDir)
	p := &installReporter{callback: callback}
	existing := false
	err := p.phase("checking", func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := os.Lstat(modelDir); err == nil {
			existing = true
			if err := verifyWakeModels(ctx, modelDir, src.models, src.vad); err != nil {
				return fmt.Errorf("existing models are damaged or custom; choose a fresh model directory: %w", err)
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if existing {
		return p.phase("complete", func() error { return nil })
	}
	parent := filepath.Dir(modelDir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".wake-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	models := filepath.Join(stage, "models")
	if err := os.Mkdir(models, 0700); err != nil {
		return err
	}
	archive := filepath.Join(stage, "model.tar.bz2")
	p.stage = "downloading-model"
	if err := downloadProgress(ctx, src.client, src.modelURL, archive, src.archive, p); err != nil {
		return err
	}
	if err := p.phase("extracting", func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractModels(contextReader{ctx, src.decode(contextReader{ctx, f})}, models)
	}); err != nil {
		return err
	}
	p.stage = "downloading-vad"
	if err := downloadProgress(ctx, src.client, src.vadURL, filepath.Join(models, src.vad.Name), src.vad, p); err != nil {
		return err
	}
	if err := p.phase("verifying", func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(models, "keywords.txt"), []byte(KeywordTokens), 0600); err != nil {
			return err
		}
		return verifyWakeModels(ctx, models, src.models, src.vad)
	}); err != nil {
		return err
	}
	manifest := struct{ Model, Binding, ArchiveSHA256, VADSHA256, Keywords string }{ModelVersion, BindingVersion, src.archive.SHA256, src.vad.SHA256, KeywordTokens}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(models, "manifest.json"), b, 0600); err != nil {
		return err
	}
	// Recheck after all synchronous callbacks: a caller may create the destination
	// while installing. Never replace even an empty custom directory.
	if _, err := os.Lstat(modelDir); err == nil {
		return fmt.Errorf("model destination appeared during installation: %s", modelDir)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(models, modelDir); err != nil {
		return fmt.Errorf("atomic model install: %w", err)
	}
	return p.phase("complete", func() error { return nil })
}

type installReporter struct {
	callback func(InstallProgress)
	stage    string
	last     time.Time
}

func (p *installReporter) report(n, total int64, force bool) {
	if p.callback == nil {
		return
	}
	now := time.Now()
	if force || now.Sub(p.last) >= 100*time.Millisecond {
		p.last = now
		p.callback(InstallProgress{Stage: p.stage, Downloaded: n, Total: total})
	}
}
func (p *installReporter) phase(stage string, f func() error) error {
	p.stage = stage
	p.report(0, 0, true)
	defer p.report(0, 0, true)
	return f()
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

type downloadReader struct {
	io.Reader
	n        *int64
	total    int64
	progress *installReporter
}

func (r *downloadReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	*r.n += int64(n)
	if r.progress != nil && *r.n < r.total {
		r.progress.report(*r.n, r.total, false)
	}
	return n, err
}
