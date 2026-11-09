package wake

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type referenceTransport func(*http.Request) (*http.Response, error)

func (f referenceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func referenceFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	oldOriginal, oldShort := referenceOriginalAsset, referenceAsset
	t.Cleanup(func() { referenceOriginalAsset, referenceAsset = oldOriginal, oldShort })
	// Include odd-sized metadata; the source PCM is not necessarily at offset 44.
	raw := make([]byte, 56+48000*6*3)
	copy(raw, "RIFF")
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(raw)-8))
	copy(raw[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(raw[16:], 16)
	binary.LittleEndian.PutUint16(raw[20:], 1)
	binary.LittleEndian.PutUint16(raw[22:], 1)
	binary.LittleEndian.PutUint32(raw[24:], 48000)
	binary.LittleEndian.PutUint32(raw[28:], 144000)
	binary.LittleEndian.PutUint16(raw[32:], 3)
	binary.LittleEndian.PutUint16(raw[34:], 24)
	copy(raw[36:], "JUNK")
	binary.LittleEndian.PutUint32(raw[40:], 3)
	copy(raw[44:], "abc")
	copy(raw[48:], "data")
	binary.LittleEndian.PutUint32(raw[52:], uint32(len(raw)-56))
	for i := 56; i < len(raw); i++ {
		raw[i] = byte((i - 56) * 31)
	}
	referenceOriginalAsset.SHA256 = sum(raw)
	referenceOriginalAsset.Size = int64(len(raw))
	short, err := cropReference(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := 48000 * ReferenceDurationMillis / 1000 * 3
	if len(short) != 44+want || !bytes.Equal(short[44:], raw[56:56+want]) || binary.LittleEndian.Uint32(short[40:]) != uint32(want) {
		t.Fatal("crop changed PCM, included metadata, or cut at wrong frame")
	}
	referenceAsset.SHA256 = sum(short)
	referenceAsset.Size = int64(len(short))
	return raw, short
}

func TestShortReferenceUpgradePreservesOriginalAndIsIdempotent(t *testing.T) {
	raw, short := referenceFixture(t)
	dir := t.TempDir()
	stage := t.TempDir()
	dst := filepath.Join(dir, referenceAsset.Name)
	if err := os.WriteFile(dst, raw, 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: referenceTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("legacy upgrade should not download")
		return nil, nil
	})}
	if err := installReference(context.Background(), client, dir, stage); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{referenceAsset.Name: short, referenceOriginalAsset.Name: raw} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s was not preserved/installed: %v", name, err)
		}
	}
	if err := VerifyAssets(dir, true); err != nil {
		t.Fatal(err)
	}
	if err := installReference(context.Background(), client, dir, stage); err != nil {
		t.Fatal(err)
	}
}

func TestFreshShortReferenceInstallVerifiesDownloadBeforeCropping(t *testing.T) {
	raw, short := referenceFixture(t)
	dir := t.TempDir()
	calls := 0
	client := &http.Client{Transport: referenceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != ReferenceURL {
			t.Error("unexpected source URL")
		}
		return &http.Response{StatusCode: 200, ContentLength: int64(len(raw)), Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})}
	if err := installReference(context.Background(), client, dir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, referenceAsset.Name))
	if err != nil || !bytes.Equal(got, short) || calls != 1 {
		t.Fatalf("fresh install: %v calls=%d", err, calls)
	}
	raw[56] ^= 1
	if err := installReference(context.Background(), client, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("unverified original accepted")
	}
}

func TestAppReferenceUpgradeOnlyTouchesKnownManagedSource(t *testing.T) {
	raw, short := referenceFixture(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, referenceAsset.Name)
	if err := os.WriteFile(dst, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeReference(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, short) {
		t.Fatal("app did not upgrade legacy sample")
	}
	original := filepath.Join(dir, referenceOriginalAsset.Name)
	if err := UpgradeReference(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(original)
	if !bytes.Equal(got, raw) {
		t.Fatal("app modified retained original")
	}
	custom := append([]byte(nil), raw...)
	custom[len(custom)-1] ^= 1
	if err := os.WriteFile(dst, custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeReference(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(dst)
	if !bytes.Equal(got, custom) {
		t.Fatal("app modified custom reference")
	}
}

func TestShortReferenceUpgradePreservesDamagedBackupAndLegacy(t *testing.T) {
	raw, _ := referenceFixture(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, referenceAsset.Name)
	original := filepath.Join(dir, referenceOriginalAsset.Name)
	if err := os.WriteFile(dst, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("custom backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installReference(context.Background(), &http.Client{}, dir, t.TempDir()); err == nil {
		t.Fatal("damaged original backup accepted")
	}
	got, _ := os.ReadFile(dst)
	backup, _ := os.ReadFile(original)
	if !bytes.Equal(got, raw) || string(backup) != "custom backup" {
		t.Fatal("overwrote source or backup on failure")
	}
}
