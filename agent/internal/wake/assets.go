package wake

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const ModelVersion = "sherpa-onnx-kws-zipformer-wenetspeech-3.3M-2024-01-01"
const modelURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/kws-models/" + ModelVersion + ".tar.bz2"
const vadURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx"
const ReferenceURL = "https://web-static.kurobbs.com/wiki_audio/1770098269166323983.wav"
const archiveSHA = "b2f7c89690dc8ce4c6ed6afeab7cd800c36ad1421fb6b6302b4a4b194cf7f35f"

type asset struct {
	Name, SHA256 string
	Size         int64
}

var modelAssets = []asset{
	{"encoder.onnx", "dd784973fc9d2fabb3b800d6dcd20fc3b0ca84f8e2415afe54b032878e447f4d", 4777666},
	{"decoder.onnx", "fb581d6734511676e246e0dff2fea01b31b0913176cb3ca64576dbab0a177774", 675349},
	{"joiner.onnx", "f79760052b87239e325f0567c752ad3130b30d92effb847d4307743c20c59a24", 65242},
	{"tokens.txt", "72316508d9119696145abc6f1f8cdc46287535c34e5ce7e595f845cb1499cf2e", 1627},
}
var vadAsset = asset{"silero_vad.onnx", "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6", 643854}
var referenceAsset = asset{"reference.wav", "34395b182a88c7371882e9cddd334bb76042c7993ad8c46245989b4b2b0049ee", 1530546}
var archiveFiles = map[string]string{
	"encoder-epoch-99-avg-1-chunk-16-left-64.int8.onnx": "encoder.onnx",
	"decoder-epoch-99-avg-1-chunk-16-left-64.onnx":      "decoder.onnx",
	"joiner-epoch-99-avg-1-chunk-16-left-64.int8.onnx":  "joiner.onnx",
	"tokens.txt": "tokens.txt",
}

func verifyFile(name string, a asset) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != a.Size {
		return fmt.Errorf("%s has unexpected type or size", a.Name)
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, a.Size+1)); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("%s SHA256 mismatch", a.Name)
	}
	return nil
}
func verifyModels(dir string) error {
	for _, a := range modelAssets {
		if err := verifyFile(filepath.Join(dir, a.Name), a); err != nil {
			return err
		}
	}
	// Never accept the model archive's multi-keyword defaults.
	a := asset{"keywords.txt", sum([]byte(KeywordTokens)), int64(len(KeywordTokens))}
	return verifyFile(filepath.Join(dir, a.Name), a)
}
func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// VerifyAssets does no network access and checks against compiled-in digests.
func VerifyAssets(dir string, referenceOnly bool) error {
	if dir == "" {
		dir = DefaultAssetDir()
	}
	if !referenceOnly {
		if err := verifyModels(filepath.Join(dir, "models")); err != nil {
			return err
		}
		if err := verifyFile(filepath.Join(dir, "models", vadAsset.Name), vadAsset); err != nil {
			return err
		}
	}
	return verifyFile(filepath.Join(dir, referenceAsset.Name), referenceAsset)
}

// InstallAssets is intentionally invoked only by the setup CLI. Each component
// is staged on the destination filesystem and renamed into place after hashes
// pass. It never modifies the user's configuration or replaces damaged models.
func InstallAssets(ctx context.Context, dir string, referenceOnly bool) error {
	if dir == "" {
		dir = DefaultAssetDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".voice-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	client := &http.Client{Timeout: 3 * time.Minute}
	if !referenceOnly {
		dst := filepath.Join(dir, "models")
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			models := filepath.Join(stage, "models")
			if err = os.Mkdir(models, 0700); err != nil {
				return err
			}
			archive := filepath.Join(stage, "model.tar.bz2")
			if err = download(ctx, client, modelURL, archive, asset{"archive", archiveSHA, 32654866}); err != nil {
				return err
			}
			f, err := os.Open(archive)
			if err != nil {
				return err
			}
			err = extractModels(bzip2.NewReader(f), models)
			f.Close()
			if err != nil {
				return err
			}
			if err = download(ctx, client, vadURL, filepath.Join(models, vadAsset.Name), vadAsset); err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(models, "keywords.txt"), []byte(KeywordTokens), 0600); err != nil {
				return err
			}
			if err = verifyModels(models); err != nil {
				return err
			}
			manifest := struct{ Model, Binding, ArchiveSHA256, VADSHA256, Keywords string }{ModelVersion, BindingVersion, archiveSHA, vadAsset.SHA256, KeywordTokens}
			b, _ := json.MarshalIndent(manifest, "", "  ")
			if err = os.WriteFile(filepath.Join(models, "manifest.json"), b, 0600); err != nil {
				return err
			}
			if err = os.Rename(models, dst); err != nil {
				return fmt.Errorf("atomic model install: %w", err)
			}
		} else {
			if err = verifyModels(dst); err != nil {
				return fmt.Errorf("existing models are damaged; choose a fresh -dir: %w", err)
			}
			if err = verifyFile(filepath.Join(dst, vadAsset.Name), vadAsset); err != nil {
				return err
			}
		}
	}
	dst := filepath.Join(dir, referenceAsset.Name)
	if _, err := os.Lstat(dst); os.IsNotExist(err) {
		tmp := filepath.Join(stage, referenceAsset.Name)
		if err = download(ctx, client, ReferenceURL, tmp, referenceAsset); err != nil {
			return err
		}
		if err = os.Rename(tmp, dst); err != nil {
			return err
		}
	} else if err = verifyFile(dst, referenceAsset); err != nil {
		return fmt.Errorf("existing reference is damaged; choose a fresh -dir: %w", err)
	}
	return VerifyAssets(dir, referenceOnly)
}
func download(ctx context.Context, c *http.Client, url, dst string, a asset) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", a.Name, resp.StatusCode)
	}
	if resp.ContentLength > a.Size {
		return fmt.Errorf("download %s exceeds pinned size", a.Name)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, a.Size+1))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != a.Size {
		return fmt.Errorf("download %s size mismatch", a.Name)
	}
	return verifyFile(dst, a)
}

// extractModels bounds the entire decompressed stream, entry count and entry
// sizes. It rejects every traversal/link/special-file entry, even unselected
// ones, and writes only four exact whitelist entries into a private directory.
func extractModels(src io.Reader, dst string) error {
	limited := &io.LimitedReader{R: src, N: 256 << 20}
	tr := tar.NewReader(limited)
	seen := map[string]bool{}
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			if limited.N <= 0 {
				return fmt.Errorf("archive decompression limit exceeded")
			}
			break
		}
		if err != nil {
			return err
		}
		if count >= 512 {
			return fmt.Errorf("too many archive entries")
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, ":") {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." || part == "." {
				return fmt.Errorf("archive traversal")
			}
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return fmt.Errorf("archive links or special files rejected")
		}
		if h.Size < 0 || h.Size > 32<<20 {
			return fmt.Errorf("archive entry too large")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if !strings.HasPrefix(name, ModelVersion+"/") {
			return fmt.Errorf("unexpected archive root")
		}
		relative := strings.TrimPrefix(name, ModelVersion+"/")
		out, ok := archiveFiles[relative]
		if !ok {
			continue
		}
		if seen[out] {
			return fmt.Errorf("duplicate model entry")
		}
		seen[out] = true
		f, err := os.OpenFile(filepath.Join(dst, out), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(f, tr, h.Size)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if len(seen) != len(archiveFiles) {
		return fmt.Errorf("model archive is incomplete")
	}
	return nil
}
