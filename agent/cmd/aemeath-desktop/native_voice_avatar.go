package main

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

// Cropped from the shared firmware sprite with go run ./scripts/avatar.
//
//go:embed assets/aemeath-avatar.png
var voiceAvatarPNG []byte

// Decode once; all portraits share one bitmap and its downscaled textures.
var voiceAvatarBitmap = func() *ui.Bitmap {
	bitmap, err := ui.DecodeBitmap(voiceAvatarPNG)
	if err != nil {
		panic(err)
	}
	return bitmap
}()

func voiceAvatar(c *ui.Context, size float32) {
	t := c.Theme()
	ui.Box(c).Size(size, size).Shrink(0).Radius(size/2).
		Background(t.Accent.Alpha(0.07)).Border(1, t.Accent.Alpha(0.12)).
		Padding(size * 0.05).Clip().Center().Children(func() {
		ui.Image(c, voiceAvatarBitmap).Fill().Fit(ui.Contain).AccessHidden(true)
	})
}
