// Command icon generates the Aemeath application icon with the Go standard library.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	const size = 1024
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	pulse := [][2]float64{{190, 535}, {350, 535}, {435, 290}, {570, 750}, {675, 450}, {745, 535}, {834, 535}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x), float64(y)
			cx, cy := math.Max(210-fx, math.Max(fx-814, 0)), math.Max(210-fy, math.Max(fy-814, 0))
			if math.Hypot(cx, cy) > 170 {
				continue
			}
			shade := uint8(19 + 11*(1-fy/size))
			col := color.NRGBA{R: shade - 6, G: shade + 9, B: shade + 8, A: 255}
			for i := 1; i < len(pulse); i++ {
				a, b := pulse[i-1], pulse[i]
				dx, dy := b[0]-a[0], b[1]-a[1]
				t := math.Max(0, math.Min(1, ((fx-a[0])*dx+(fy-a[1])*dy)/(dx*dx+dy*dy)))
				if math.Hypot(fx-a[0]-t*dx, fy-a[1]-t*dy) < 27 {
					col = color.NRGBA{R: 109, G: 231, B: 197, A: 255}
					break
				}
			}
			img.SetNRGBA(x, y, col)
		}
	}
	if err := os.MkdirAll("resources", 0755); err != nil {
		panic(err)
	}
	f, err := os.Create("resources/icon.png")
	if err != nil {
		panic(err)
	}
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
}
