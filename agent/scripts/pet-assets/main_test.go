package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratorReproducesCommittedEvidence(t *testing.T) {
	out := t.TempDir()
	if err := generate("../../../firmware/main/assets/avatar_source/spritesheet.webp", out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"atlas.png", "p0-black.png", "p0-white.png", "validation.json"} {
		generated, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join("../../internal/pet/assets", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(generated, committed) {
			t.Fatalf("%s is not reproducible; run go generate ./internal/pet", name)
		}
	}
}
func TestGeneratorInvalidInputDoesNotCreateArtifacts(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "output")
	if err := generate(filepath.Join(dir, "missing.webp"), out); err == nil {
		t.Fatal("missing input accepted")
	}
	source := filepath.Join(dir, "invalid.webp")
	if err := os.WriteFile(source, []byte("invalid webp"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generate(source, out); err == nil {
		t.Fatal("invalid WebP accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("wrote output before verification")
	}
}
