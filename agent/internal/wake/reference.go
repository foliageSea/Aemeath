package wake

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// cropReference copies complete PCM frames; no architecture-dependent float DSP
// participates in the pinned short-file digest. The chosen endpoint sits in the
// original 4.02–4.76s pause, retaining 180ms of tail silence.
func cropReference(raw []byte) ([]byte, error) {
	if int64(len(raw)) != referenceOriginalAsset.Size || sum(raw) != referenceOriginalAsset.SHA256 {
		return nil, fmt.Errorf("original reference checksum mismatch")
	}
	var format, data []byte
	if len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, fmt.Errorf("invalid original reference WAV")
	}
	for offset := 12; offset+8 <= len(raw); {
		n := int(binary.LittleEndian.Uint32(raw[offset+4:]))
		start := offset + 8
		if n > len(raw)-start {
			return nil, fmt.Errorf("invalid reference chunk length")
		}
		switch string(raw[offset : offset+4]) {
		case "fmt ":
			format = raw[start : start+n]
		case "data":
			data = raw[start : start+n]
		}
		offset = start + n + (n & 1)
	}
	if len(format) < 16 || binary.LittleEndian.Uint16(format) != 1 || binary.LittleEndian.Uint16(format[2:]) != 1 || binary.LittleEndian.Uint32(format[4:]) != 48000 || binary.LittleEndian.Uint16(format[12:]) != 3 || binary.LittleEndian.Uint16(format[14:]) != 24 {
		return nil, fmt.Errorf("unexpected reference PCM format")
	}
	n := 48000 * ReferenceDurationMillis / 1000 * 3
	if len(data) < n {
		return nil, fmt.Errorf("reference shorter than crop")
	}
	result := make([]byte, 44+n)
	copy(result, "RIFF")
	binary.LittleEndian.PutUint32(result[4:], uint32(len(result)-8))
	copy(result[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(result[16:], 16)
	copy(result[20:], format[:16])
	copy(result[36:], "data")
	binary.LittleEndian.PutUint32(result[40:], uint32(n))
	copy(result[44:], data[:n])
	return result, nil
}

// UpgradeReference locally migrates only the exact legacy managed sample. It
// never downloads, touches unknown custom audio, or shortens the saved original.
// This lets an updated app adopt the short default without a separate CLI run.
func UpgradeReference(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if filepath.Base(name) != referenceAsset.Name {
		return nil
	}
	st, err := os.Stat(name)
	if err != nil || st.Size() != referenceOriginalAsset.Size {
		return nil
	}
	if verifyFile(name, referenceOriginalAsset) != nil {
		return nil
	}
	dir := filepath.Dir(name)
	stage, err := os.MkdirTemp(dir, ".reference-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	return installReference(ctx, nil, dir, stage)
}

func installReference(ctx context.Context, client *http.Client, dir, stage string) error {
	dst := filepath.Join(dir, referenceAsset.Name)
	if verifyFile(dst, referenceAsset) == nil {
		return nil
	}
	_, statErr := os.Lstat(dst)
	legacy := statErr == nil
	if legacy {
		if err := verifyFile(dst, referenceOriginalAsset); err != nil {
			return fmt.Errorf("existing reference is damaged; choose a fresh -dir: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	original := filepath.Join(dir, referenceOriginalAsset.Name)
	if _, err := os.Lstat(original); os.IsNotExist(err) {
		tmp := filepath.Join(stage, referenceOriginalAsset.Name)
		if legacy {
			raw, e := os.ReadFile(dst)
			if e != nil {
				return e
			}
			if e = os.WriteFile(tmp, raw, 0600); e != nil {
				return e
			}
			if e = verifyFile(tmp, referenceOriginalAsset); e != nil {
				return e
			}
		} else {
			if client == nil {
				return fmt.Errorf("original reference missing; run asset installer")
			}
			if err = download(ctx, client, ReferenceURL, tmp, referenceOriginalAsset); err != nil {
				return err
			}
		}
		if err = os.Rename(tmp, original); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := os.ReadFile(original)
	if err != nil {
		return err
	}
	clipped, err := cropReference(raw)
	if err != nil {
		return err
	}
	tmp := filepath.Join(stage, referenceAsset.Name)
	if err = os.WriteFile(tmp, clipped, 0600); err != nil {
		return err
	}
	if err = verifyFile(tmp, referenceAsset); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Recheck immediately before replacing only the known legacy asset.
	if legacy {
		if err = verifyFile(dst, referenceOriginalAsset); err != nil {
			return err
		}
	} else if _, e := os.Lstat(dst); !os.IsNotExist(e) {
		return fmt.Errorf("reference appeared during install; refusing to replace it")
	}
	return os.Rename(tmp, dst)
}
