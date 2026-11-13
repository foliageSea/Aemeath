package background

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceFile(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func solid(w, h int, c color.Color) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return m
}
func jpegBytes(t *testing.T, src image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestLoadPNGAndJPEGKeepSourceDimensions(t *testing.T) {
	src := solid(73, 29, color.RGBA{R: 80, G: 120, B: 200, A: 255})
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			var b bytes.Buffer
			if format == "png" {
				if err := png.Encode(&b, src); err != nil {
					t.Fatal(err)
				}
			} else {
				b.Write(jpegBytes(t, src))
			}
			data := bytes.Clone(b.Bytes())
			path := sourceFile(t, data)
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Bounds().Dx() != 73 || loaded.Bounds().Dy() != 29 {
				t.Fatal(loaded.Bounds())
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("source file modified")
			}
		})
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	transparent.SetNRGBA(1, 1, color.NRGBA{R: 230, G: 20, B: 30, A: 50})
	var b bytes.Buffer
	_ = png.Encode(&b, transparent)
	loaded, err := Load(sourceFile(t, b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if color.NRGBAModel.Convert(loaded.At(1, 1)) != (color.NRGBA{R: 230, G: 20, B: 30, A: 50}) {
		t.Fatal("Load flattened alpha")
	}
}

// A small static lossless WebP fixture from golang.org/x/image testdata,
// Copyright The Go Authors, BSD-3-Clause (gopher-doc.1bpp.lossless.webp).
const staticWebPFixture = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="

func TestLoadStaticWebPRejectAnimation(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(staticWebPFixture)
	if err != nil {
		t.Fatal(err)
	}
	src, err := Load(sourceFile(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if src.Bounds().Empty() {
		t.Fatal("empty WebP")
	}
	// Add the VP8X animation flag before the otherwise valid VP8L chunk.
	chunk := []byte{'V', 'P', '8', 'X', 10, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	animated := append(bytes.Clone(data[:12]), chunk...)
	animated = append(animated, data[12:]...)
	binary.LittleEndian.PutUint32(animated[4:8], uint32(len(animated)-8))
	if _, err := Load(sourceFile(t, animated)); err == nil || !strings.Contains(err.Error(), "动态") {
		t.Fatalf("animation accepted: %v", err)
	}
	for _, bad := range [][]byte{data[:len(data)-1], append(bytes.Clone(data), 0), []byte("RIFFxxxxWEBPxxxx")} {
		if err := staticWebP(bad); err == nil {
			t.Fatal("malformed WebP accepted")
		}
	}
}

func TestLoadRejectsFormatSizeAndDimensionBeforeDecode(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
	for _, data := range [][]byte{nil, []byte("<html>old firmware</html>"), {255, 216, 255}} {
		if _, err := Load(sourceFile(t, data)); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	var g bytes.Buffer
	_ = gif.Encode(&g, solid(2, 3, color.White), nil)
	if _, err := Load(sourceFile(t, g.Bytes())); err == nil {
		t.Fatal("GIF accepted")
	}
	path := sourceFile(t, nil)
	if err := os.Truncate(path, MaxSourceBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatal(err)
	}
	var p bytes.Buffer
	_ = png.Encode(&p, solid(1, 1, color.White))
	for _, size := range [][2]uint32{{16385, 1}, {8001, 4000}, {0, 1}} {
		data := bytes.Clone(p.Bytes())
		binary.BigEndian.PutUint32(data[16:20], size[0])
		binary.BigEndian.PutUint32(data[20:24], size[1])
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		if _, err := Load(sourceFile(t, data)); err == nil {
			t.Fatalf("size %v accepted", size)
		}
	}
	if err := dimensions(8000, 4000); err != nil {
		t.Fatal(err)
	}
	if err := dimensions(16384, 1); err != nil {
		t.Fatal(err)
	}
}

func exifJPEG(t *testing.T, data []byte, value int, order binary.ByteOrder) []byte {
	t.Helper()
	p := make([]byte, 32)
	copy(p, []byte("Exif\x00\x00"))
	if order == binary.LittleEndian {
		copy(p[6:], "II")
	} else {
		copy(p[6:], "MM")
	}
	order.PutUint16(p[8:10], 42)
	order.PutUint32(p[10:14], 8)
	order.PutUint16(p[14:16], 1)
	order.PutUint16(p[16:18], 0x112)
	order.PutUint16(p[18:20], 3)
	order.PutUint32(p[20:24], 1)
	order.PutUint16(p[24:26], uint16(value))
	out := append(bytes.Clone(data[:2]), 255, 225, byte((len(p)+2)>>8), byte(len(p)+2))
	out = append(out, p...)
	return append(out, data[2:]...)
}

func TestEXIFAllOrientationsAndPixelMapping(t *testing.T) {
	src := image.NewNRGBA(image.Rect(-3, 7, 0, 9))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			src.SetNRGBA(x-3, y+7, color.NRGBA{R: uint8(1 + x + y*3), A: 255})
		}
	}
	expected := [][]uint8{{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3}, {1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4}}
	for orientation := 1; orientation <= 8; orientation++ {
		transformed := orient(src, orientation)
		b := transformed.Bounds()
		i := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r := color.NRGBAModel.Convert(transformed.At(x, y)).(color.NRGBA).R
				if r != expected[orientation-1][i] {
					t.Fatalf("orientation %d pixel %d = %d", orientation, i, r)
				}
				i++
			}
		}
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			data := exifJPEG(t, jpegBytes(t, src), orientation, order)
			got, err := jpegOrientation(data)
			if err != nil || got != orientation {
				t.Fatalf("orientation %d: %d %v", orientation, got, err)
			}
			loaded, err := Load(sourceFile(t, data))
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Bounds().Dx() != b.Dx() || loaded.Bounds().Dy() != b.Dy() {
				t.Fatal("orientation dimensions")
			}
		}
	}
	data := exifJPEG(t, jpegBytes(t, src), 9, binary.LittleEndian)
	if _, err := Load(sourceFile(t, data)); err == nil {
		t.Fatal("invalid orientation accepted")
	}
	valid := exifJPEG(t, jpegBytes(t, src), 1, binary.LittleEndian)
	binary.LittleEndian.PutUint32(valid[16:20], math.MaxUint32) // corrupt TIFF IFD offset
	if _, err := Load(sourceFile(t, valid)); err == nil {
		t.Fatal("bad EXIF offset accepted")
	}
}

func TestCropRectPanZoomClampAndNonzeroBounds(t *testing.T) {
	e := DefaultEdit()
	b := image.Rect(-10, 20, 790, 420)
	if got := CropRect(b, e); got != image.Rect(190, 20, 590, 420) {
		t.Fatal(got)
	}
	e.Zoom = 2
	e.X = 0
	e.Y = 1
	if got := CropRect(b, e); got != image.Rect(-10, 220, 190, 420) {
		t.Fatal(got)
	}
	e.Zoom = 4
	e.X = 1
	e.Y = 0
	if got := CropRect(b, e); got != image.Rect(690, 20, 790, 120) {
		t.Fatal(got)
	}
	for _, v := range []float64{-100, 100, math.NaN(), math.Inf(1), math.Inf(-1)} {
		e.Zoom, e.X, e.Y = v, v, v
		r := CropRect(b, e)
		if !r.In(b) || r.Dx() != r.Dy() || r.Empty() {
			t.Fatalf("unsafe crop %v: %v", v, r)
		}
	}
	if r := CropRect(image.Rectangle{}, e); !r.Empty() {
		t.Fatal(r)
	}
	if r := CropRect(image.Rect(0, 0, 1, 1), DefaultEdit()); r != image.Rect(0, 0, 1, 1) {
		t.Fatal(r)
	}
}

func TestRenderStrictValuesAndBounds(t *testing.T) {
	src := solid(5, 3, color.White)
	edits := []Edit{{}}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 201} {
		e := DefaultEdit()
		e.Zoom = v
		if v != 201 && v != -1 || e.Zoom > 4 {
			edits = append(edits, e)
		}
		e = DefaultEdit()
		e.X = v
		edits = append(edits, e)
		e = DefaultEdit()
		e.Y = v
		edits = append(edits, e)
		e = DefaultEdit()
		e.Brightness = v
		edits = append(edits, e)
		e = DefaultEdit()
		e.Contrast = v
		edits = append(edits, e)
	}
	for _, q := range []int{-1, 0, 39, 96, 100} {
		e := DefaultEdit()
		e.Quality = q
		edits = append(edits, e)
	}
	for i, e := range edits {
		if _, err := Render(src, e); err == nil {
			t.Fatalf("edit %d accepted: %+v", i, e)
		}
	}
	for _, src := range []image.Image{nil, (*image.RGBA)(nil), image.NewRGBA(image.Rectangle{}), image.NewUniform(color.White)} {
		if _, err := Render(src, DefaultEdit()); err == nil {
			t.Fatal("bad source accepted")
		}
	}
}

func TestRenderFinalPreviewTransparencyAndNoCumulativeLoss(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 10, 7))
	copyBefore := bytes.Clone(source.Pix)
	result, err := Render(source, DefaultEdit())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateJPEG(result.JPEG); err != nil {
		t.Fatal(err)
	}
	p := result.Image.RGBAAt(200, 200)
	if abs(int(p.R)-11) > 2 || abs(int(p.G)-15) > 2 || abs(int(p.B)-20) > 2 || p.A != 255 {
		t.Fatal(p)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(result.JPEG))
	if err != nil {
		t.Fatal(err)
	}
	preview := image.NewRGBA(result.Image.Bounds())
	draw.Draw(preview, preview.Bounds(), decoded, image.Point{}, draw.Src)
	if !bytes.Equal(result.Image.Pix, preview.Pix) {
		t.Fatal("preview is not final JPEG")
	}
	modified := DefaultEdit()
	modified.Brightness = 200
	modified.Contrast = 20
	if _, err := Render(source, modified); err != nil {
		t.Fatal(err)
	}
	again, err := Render(source, DefaultEdit())
	if err != nil || !bytes.Equal(result.JPEG, again.JPEG) || !bytes.Equal(source.Pix, copyBefore) {
		t.Fatal("cumulative loss/source mutation")
	}
	white := solid(3, 3, color.RGBA{R: 80, G: 120, B: 160, A: 255})
	e := DefaultEdit()
	e.Brightness = 0
	black, err := Render(white, e)
	if err != nil {
		t.Fatal(err)
	}
	if black.Image.RGBAAt(10, 10) != (color.RGBA{A: 255}) {
		t.Fatal("zero brightness")
	}
	e = DefaultEdit()
	e.Contrast = 0
	gray, err := Render(white, e)
	if err != nil {
		t.Fatal(err)
	}
	if abs(int(gray.Image.RGBAAt(0, 0).R)-128) > 1 {
		t.Fatal("zero contrast")
	}
}

func noiseImage() *image.RGBA {
	src := image.NewRGBA(image.Rect(0, 0, Size, Size))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < len(src.Pix); i += 4 {
		v := rng.Uint32()
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = byte(v), byte(v>>8), byte(v>>16), 255
	}
	return src
}
func TestRenderCompressionQualityFallsInStepsOfFive(t *testing.T) {
	e := DefaultEdit()
	e.Quality = 94
	src := noiseImage()
	result, err := Render(src, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.JPEG) > MaxBytes || result.Quality >= 94 || result.Quality < 40 || (94-result.Quality)%5 != 0 && result.Quality != 40 {
		t.Fatalf("quality=%d bytes=%d", result.Quality, len(result.JPEG))
	}
	var previous bytes.Buffer
	_ = jpeg.Encode(&previous, src, &jpeg.Options{Quality: result.Quality + 5})
	if previous.Len() <= MaxBytes {
		t.Fatal("unnecessary quality reduction")
	}
	e.Quality = 40
	result, err = Render(src, e)
	if err != nil || result.Quality != 40 {
		t.Fatalf("floor: %v", err)
	}
}

func TestValidateJPEGRejectsWrongSizeProgressiveAndTruncation(t *testing.T) {
	valid := jpegBytes(t, solid(Size, Size, color.White))
	if err := ValidateJPEG(valid); err != nil {
		t.Fatal(err)
	}
	progressive := bytes.Clone(valid)
	i := bytes.Index(progressive, []byte{255, 192})
	if i < 0 {
		t.Fatal("no SOF")
	}
	progressive[i+1] = 194
	for _, data := range [][]byte{nil, bytes.Repeat([]byte{0}, MaxBytes+1), valid[:len(valid)-1], append(bytes.Clone(valid), 0), jpegBytes(t, solid(481, 480, color.White)), progressive, {255, 216, 255, 217}} {
		if err := ValidateJPEG(data); err == nil {
			t.Fatal("invalid wire JPEG accepted")
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
