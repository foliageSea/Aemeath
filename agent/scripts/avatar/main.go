// Extract the desktop portrait from the firmware's shared chibi artwork.
// Run from agent/: go run ./scripts/avatar
package main

import (
	"image"
	"image/draw"
	"image/png"
	"os"

	"golang.org/x/image/webp"
)

func main() {
	source, err := os.Open("../firmware/main/assets/avatar_source/spritesheet.webp")
	check(err)
	defer source.Close()
	sheet, err := webp.Decode(source)
	check(err)
	// The first idle frame is 192×208. Keep the face, cub and held cat.
	portrait := image.NewNRGBA(image.Rect(0, 0, 160, 160))
	draw.Draw(portrait, portrait.Bounds(), sheet, image.Pt(16, 0), draw.Src)
	check(os.MkdirAll("cmd/aemeath-desktop/assets", 0755))
	out, err := os.Create("cmd/aemeath-desktop/assets/aemeath-avatar.png")
	check(err)
	check(png.Encode(out, portrait))
	check(out.Close())
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
