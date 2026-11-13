package background

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"testing"
)

func padJPEG(data []byte, length int) []byte {
	out := bytes.Clone(data[:2])
	for remaining := length - len(data); remaining > 0; {
		chunk := min(remaining, 65537)
		if rest := remaining - chunk; rest > 0 && rest < 4 {
			chunk -= 4
		}
		segment := make([]byte, chunk)
		segment[0], segment[1] = 255, 226
		binary.BigEndian.PutUint16(segment[2:4], uint16(chunk-2))
		out = append(out, segment...)
		remaining -= chunk
	}
	return append(out, data[2:]...)
}

func TestWireJPEGExactSizeBoundaryAndNoConcatenation(t *testing.T) {
	data := jpegBytes(t, solid(Size, Size, color.White))
	padded := padJPEG(data, MaxBytes)
	if len(padded) != MaxBytes {
		t.Fatal(len(padded))
	}
	if err := ValidateJPEG(padded); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{padJPEG(data, MaxBytes+1), append(bytes.Clone(data), data...), append(append(bytes.Clone(data), []byte("<html>")...), 255, 217)} {
		if err := ValidateJPEG(bad); err == nil {
			t.Fatal("oversize or hidden trailing content accepted")
		}
	}
}

func TestLoadSourceByteLimitInclusive(t *testing.T) {
	data := jpegBytes(t, solid(3, 2, color.White))
	// Source images can carry unrelated trailing metadata; byte admission still
	// needs to distinguish exactly 20 MiB from one byte beyond it.
	data = append(data, make([]byte, MaxSourceBytes-len(data))...)
	path := sourceFile(t, data)
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Bounds().Size() != (image.Point{X: 3, Y: 2}) {
		t.Fatal(loaded.Bounds())
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte{0})
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("source byte limit not enforced")
	}
}

func FuzzUntrustedJPEGHeaders(f *testing.F) {
	for _, seed := range [][]byte{nil, {255, 216, 255, 217}, []byte("Exif\x00\x00II"), {255, 216, 255, 225, 255, 255}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxBytes {
			return
		}
		_, _ = jpegOrientation(data)
		_, _, _ = tiffOrientation(data)
		_ = ValidateJPEG(data)
		_ = staticWebP(data)
	})
}
