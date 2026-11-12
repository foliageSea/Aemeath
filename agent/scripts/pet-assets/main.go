// Command pet-assets decodes the original WebP with the existing Go x/image decoder.
// Run from agent: go run ./scripts/pet-assets
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/webp"
)

const cellW, cellH, columns = 192, 208, 8

var names = []string{"idle", "running-right", "running-left", "waving", "jumping", "failed", "waiting", "running", "review"}
var counts = []int{6, 8, 8, 4, 5, 8, 6, 6, 6}

type cellEvidence struct {
	Action         string          `json:"action"`
	Row            int             `json:"row"`
	Column         int             `json:"column"`
	Used           bool            `json:"used"`
	Nontransparent int             `json:"nontransparent_pixels"`
	Bounds         image.Rectangle `json:"bounds"`
	EdgePixels     int             `json:"edge_pixels"`
}
type report struct {
	SourceSHA256           string         `json:"source_sha256"`
	Width                  int            `json:"width"`
	Height                 int            `json:"height"`
	CellWidth              int            `json:"cell_width"`
	CellHeight             int            `json:"cell_height"`
	UsedCells              int            `json:"used_cells"`
	UnusedTransparentCells int            `json:"unused_transparent_cells"`
	TransparentRGBZero     bool           `json:"transparent_rgb_zero"`
	Uncropped              bool           `json:"uncropped"`
	Cells                  []cellEvidence `json:"cells"`
}

func main() {
	source := flag.String("source", "../firmware/main/assets/avatar_source/spritesheet.webp", "original source WebP")
	out := flag.String("out", "internal/pet/assets", "output directory")
	flag.Parse()
	if err := generate(*source, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(source, out string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	src, err := webp.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	if src.Bounds() != image.Rect(0, 0, columns*cellW, len(counts)*cellH) {
		return fmt.Errorf("unexpected atlas bounds %v", src.Bounds())
	}
	atlas := image.NewNRGBA(src.Bounds())
	draw.Draw(atlas, atlas.Bounds(), src, src.Bounds().Min, draw.Src)
	for i := 0; i < len(atlas.Pix); i += 4 {
		if atlas.Pix[i+3] == 0 {
			atlas.Pix[i], atlas.Pix[i+1], atlas.Pix[i+2] = 0, 0, 0
		}
	}
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(sourceBytes)
	evidence := report{SourceSHA256: hex.EncodeToString(hash[:]), Width: atlas.Bounds().Dx(), Height: atlas.Bounds().Dy(), CellWidth: cellW, CellHeight: cellH, TransparentRGBZero: true, Uncropped: true}
	for row, n := range counts {
		for col := 0; col < columns; col++ {
			c := cellEvidence{Action: names[row], Row: row, Column: col, Used: col < n}
			for y := 0; y < cellH; y++ {
				for x := 0; x < cellW; x++ {
					if atlas.NRGBAAt(col*cellW+x, row*cellH+y).A == 0 {
						continue
					}
					c.Nontransparent++
					pixel := image.Rect(x, y, x+1, y+1)
					if c.Nontransparent == 1 {
						c.Bounds = pixel
					} else {
						c.Bounds = c.Bounds.Union(pixel)
					}
					if x == 0 || x == cellW-1 || y == 0 || y == cellH-1 {
						c.EdgePixels++
					}
				}
			}
			if c.Used {
				if c.Nontransparent == 0 || c.EdgePixels != 0 {
					return fmt.Errorf("used cell %s/%d empty or clipped: %+v", c.Action, col, c)
				}
				evidence.UsedCells++
			} else {
				if c.Nontransparent != 0 {
					return fmt.Errorf("unused cell %s/%d is not transparent", c.Action, col)
				}
				evidence.UnusedTransparentCells++
			}
			evidence.Cells = append(evidence.Cells, c)
		}
	}
	if evidence.UsedCells != 57 || evidence.UnusedTransparentCells != 15 {
		return fmt.Errorf("incorrect cell counts: %d/%d", evidence.UsedCells, evidence.UnusedTransparentCells)
	}
	if err = os.MkdirAll(out, 0755); err != nil {
		return err
	}
	if err = writePNG(filepath.Join(out, "atlas.png"), atlas); err != nil {
		return err
	}
	for _, bg := range []struct {
		name string
		c    color.NRGBA
	}{{"black", color.NRGBA{A: 255}}, {"white", color.NRGBA{R: 255, G: 255, B: 255, A: 255}}} {
		// The full atlas is composited at 1:1, with labels in a separate left gutter.
		proof := image.NewNRGBA(image.Rect(0, 0, atlas.Bounds().Dx()+128, atlas.Bounds().Dy()+24))
		draw.Draw(proof, proof.Bounds(), image.NewUniform(bg.c), image.Point{}, draw.Src)
		draw.Draw(proof, atlas.Bounds().Add(image.Pt(128, 24)), atlas, image.Point{}, draw.Over)
		ink := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
		if bg.name == "white" {
			ink = color.NRGBA{A: 255}
		}
		d := font.Drawer{Dst: proof, Src: image.NewUniform(ink), Face: basicfont.Face7x13, Dot: fixed.P(6, 16)}
		d.DrawString("57 frames / 15 empty / 192x208 / no cropping / " + bg.name)
		for row, name := range names {
			d.Dot = fixed.P(6, row*cellH+40)
			d.DrawString(fmt.Sprintf("%d %s", row, name))
			d.Dot = fixed.P(6, row*cellH+56)
			d.DrawString(fmt.Sprintf("%d frames", counts[row]))
		}
		if err = writePNG(filepath.Join(out, "p0-"+bg.name+".png"), proof); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(out, "validation.json"), append(b, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("P0 verified: %d used cells; %d transparent unused cells; 192x208 untrimmed; transparent RGB=0; %s\n", evidence.UsedCells, evidence.UnusedTransparentCells, out)
	return nil
}
func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	err = encoder.Encode(f, img)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
