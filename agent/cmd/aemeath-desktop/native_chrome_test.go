package main

import (
	"fmt"
	"math"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeTitleBarReservesSystemButtons(t *testing.T) {
	for _, room := range []ui.TitleBar{
		{},                                 // Headless or no system buttons.
		{Height: 48, Left: 90},             // macOS traffic lights.
		{Height: 48, Right: 138},           // Windows caption buttons.
		{Height: 60, Left: 100, Right: 50}, // Desktop-supplied controls.
	} {
		for _, size := range [][2]int{{760, 600}, {1100, 800}} {
			t.Run(fmt.Sprintf("room=%v/size=%v", room, size), func(t *testing.T) {
				_, _, _, tt := nativeTest(t)
				tt.SetSize(size[0], size[1])
				tt.SetTitleBar(room)
				bar, ok := tt.Find("窗口标题栏")
				if !ok || bar.X != 0 || bar.Y != 0 || bar.W != float32(size[0]) || bar.H != max(float32(nativeTitleBarHeight), room.Height) {
					t.Fatalf("title bar does not cover native controls: %+v", bar)
				}
				title, ok := tt.Find("Aemeath")
				if !ok || title.X < room.Left || title.X+title.W > bar.W-room.Right || title.Y < 0 || title.Y+title.H > bar.H {
					t.Fatalf("title overlaps system controls: %+v", title)
				}
				if math.Abs(float64(title.X+title.W/2-bar.W/2)) > 1 {
					t.Fatalf("title is not centered in window: %+v", title)
				}
				nav, ok := tt.AccessByRole(ui.RoleButton, "概览")
				if !ok || nav.Bounds.Y < bar.H {
					t.Fatalf("sidebar starts under system controls: %+v", nav)
				}
				nativeClick(t, tt, "设置")
				field, ok := tt.Find("设备地址")
				if !ok || field.Y < bar.H || field.Y+field.H > float32(size[1]) {
					t.Fatalf("settings field falls outside content: %+v", field)
				}
			})
		}
	}
}

// Exercise the Windows paint policy on every host: headless tests cannot draw
// DWM's glyphs, but can ensure no underlying pixel shows through the button area.
func TestNativeTitleBarBackgroundCoversWindowsSystemGlyphs(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dark=%v", goos, dark), func(t *testing.T) {
				tt := ui.NewTester(func(c *ui.Context) {
					c.Root().Background(ui.Transparent)
					ui.Column(c).Fill().Background(nativeTitleBarBackground(c, goos))
				}, 138, nativeTitleBarHeight)
				tt.SetDark(dark)
				for _, state := range []struct{ material, reduceTransparency bool }{
					{false, false}, {true, false}, {true, true}, {true, false}, {false, false},
				} {
					tt.SetVibrancy(state.material)
					tt.SetPreferences(ui.Preferences{ReduceTransparency: state.reduceTransparency})
					img := tt.Image()
					translucent := goos != "windows" && state.material && !state.reduceTransparency
					for y := 0; y < img.Bounds().Dy(); y++ {
						for x := 0; x < img.Bounds().Dx(); x++ {
							alpha := img.RGBAAt(x, y).A
							if translucent && (alpha == 0 || alpha == 255) || !translucent && alpha != 255 {
								t.Fatalf("material=%v reduceTransparency=%v pixel=(%d,%d) alpha=%d", state.material, state.reduceTransparency, x, y, alpha)
							}
						}
					}
				}
			})
		}
	}
}

func TestNativeChromeMaterialOnlyShowsThroughChrome(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%v", dark), func(t *testing.T) {
			_, _, _, tt := nativeTest(t)
			tt.SetSize(1100, 800)
			tt.SetDark(dark)
			// Toggle materials and accessibility preferences to catch stale
			// transparent/opaque backgrounds when either changes at runtime.
			for _, state := range []struct{ material, reduceTransparency bool }{
				{false, false}, {true, false}, {true, true}, {true, false}, {false, false},
			} {
				tt.SetVibrancy(state.material)
				tt.SetPreferences(ui.Preferences{ReduceTransparency: state.reduceTransparency})
				material := state.material && !state.reduceTransparency
				img := tt.Image()
				for _, point := range [][2]int{{240, 20}, {4, 300}} {
					alpha := img.RGBAAt(point[0], point[1]).A
					translucent := material && (point[1] >= nativeTitleBarHeight || runtime.GOOS != "windows")
					if translucent && (alpha == 0 || alpha == 255) || !translucent && alpha != 255 {
						t.Fatalf("translucent=%v chrome pixel=%v alpha=%d", translucent, point, alpha)
					}
				}
				if alpha := img.RGBAAt(210, nativeTitleBarHeight+4).A; alpha != 255 {
					t.Fatalf("main content must stay opaque, alpha=%d", alpha)
				}
			}
		})
	}
}
