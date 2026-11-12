package pet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"os"
	"testing"

	"golang.org/x/image/webp"
)

func TestAtlasMatchesOriginalWithoutCropping(t *testing.T) {
	atlas, err := png.Decode(bytes.NewReader(AtlasPNG))
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../../firmware/main/assets/avatar_source/spritesheet.webp")
	if err != nil {
		t.Fatal(err)
	}
	original, err := webp.Decode(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	if atlas.Bounds() != original.Bounds() || atlas.Bounds() != image.Rect(0, 0, 1536, 1872) {
		t.Fatalf("changed atlas bounds: %v", atlas.Bounds())
	}
	expected := image.NewNRGBA(original.Bounds())
	draw.Draw(expected, expected.Bounds(), original, image.Point{}, draw.Src)
	actual := image.NewNRGBA(atlas.Bounds())
	draw.Draw(actual, actual.Bounds(), atlas, image.Point{}, draw.Src)
	for i := 0; i < len(expected.Pix); i += 4 {
		if expected.Pix[i+3] == 0 {
			expected.Pix[i], expected.Pix[i+1], expected.Pix[i+2] = 0, 0, 0
		}
		if !bytes.Equal(expected.Pix[i:i+4], actual.Pix[i:i+4]) {
			t.Fatalf("pixel differs at (%d,%d): %v != %v", (i/4)%1536, (i/4)/1536, actual.Pix[i:i+4], expected.Pix[i:i+4])
		}
	}
	used, unused := 0, 0
	for _, row := range Rows {
		for col := 0; col < AtlasColumns; col++ {
			pixels := 0
			for y := 0; y < FrameHeight; y++ {
				for x := 0; x < FrameWidth; x++ {
					c := actual.NRGBAAt(col*FrameWidth+x, row.Row*FrameHeight+y)
					if c.A == 0 {
						if c.R != 0 || c.G != 0 || c.B != 0 {
							t.Fatal("transparent RGB is not zero")
						}
						continue
					}
					pixels++
					if col < row.Frames && (x == 0 || y == 0 || x == FrameWidth-1 || y == FrameHeight-1) {
						t.Fatalf("clipped frame %s/%d", row.Action, col)
					}
				}
			}
			if col < row.Frames {
				used++
				if pixels == 0 {
					t.Fatalf("empty used frame %s/%d", row.Action, col)
				}
			} else {
				unused++
				if pixels != 0 {
					t.Fatalf("nonempty unused frame %s/%d", row.Action, col)
				}
			}
		}
	}
	if used != 57 || unused != 15 {
		t.Fatalf("cells: %d used/%d unused", used, unused)
	}
	b, err := os.ReadFile("assets/validation.json")
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		SourceSHA256 string `json:"source_sha256"`
		Used         int    `json:"used_cells"`
		Unused       int    `json:"unused_transparent_cells"`
		Uncropped    bool   `json:"uncropped"`
		Zero         bool   `json:"transparent_rgb_zero"`
	}
	if err = json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(source)
	if report.SourceSHA256 != hex.EncodeToString(hash[:]) || report.Used != 57 || report.Unused != 15 || !report.Uncropped || !report.Zero {
		t.Fatalf("invalid P0 evidence: %+v", report)
	}
}
func TestFrameManifest(t *testing.T) {
	if len(Rows) != 9 {
		t.Fatal("expected nine actions")
	}
	frames := 0
	for i, row := range Rows {
		if row.Row != i || row.FrameDuration < 140000000 || row.FrameDuration > 150000000 {
			t.Fatalf("invalid row %+v", row)
		}
		for j := 0; j < row.Frames; j++ {
			r := Frame(row.Action, j)
			if r != image.Rect(j*192, i*208, (j+1)*192, (i+1)*208) {
				t.Fatal(r)
			}
			frames++
		}
		for _, j := range []int{-1, row.Frames, 100} {
			if !Frame(row.Action, j).Empty() {
				t.Fatal("unused cell exposed")
			}
		}
	}
	if frames != 57 || !Frame("missing", 0).Empty() {
		t.Fatal("invalid manifest")
	}
}
func TestP0CompositesKeepFullAtlas(t *testing.T) {
	for _, name := range []string{"black", "white"} {
		f, err := os.Open("assets/p0-" + name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds() != image.Rect(0, 0, 1664, 1896) {
			t.Fatal("evidence image cropped")
		}
		for _, p := range []image.Point{{1663, 1895}, {1500, 100}, {128, 24}} {
			r, g, b, a := img.At(p.X, p.Y).RGBA()
			want := uint32(0)
			if name == "white" {
				want = 65535
			}
			if a != 65535 || r != want || g != want || b != want {
				t.Fatalf("bad %s background at %v", name, p)
			}
		}
	}
}
