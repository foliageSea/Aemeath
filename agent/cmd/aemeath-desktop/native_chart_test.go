package main

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSparklineRendersDiagonalInsteadOfAxisAlignedEdges(t *testing.T) {
	a := &nativeUI{history: []nativeSample{{ts: 1, values: [5]float64{0}}, {ts: 2, values: [5]float64{100}}}}
	tt := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.sparkline(c, 0, ui.RGB(255, 0, 0)) }) }, 100, 46)
	img := tt.Image()
	redAt := func(x, y int) bool { p := img.RGBAAt(x, y); return p.R > 150 && p.G < 100 && p.B < 100 }
	middle := false
	for y := 21; y <= 25; y++ {
		for x := 48; x <= 52; x++ {
			middle = middle || redAt(x, y)
		}
	}
	if !middle {
		t.Fatal("diagonal does not pass through the midpoint")
	}
	if redAt(50, 4) || redAt(4, 23) {
		t.Fatal("diagonal rendered as horizontal or vertical bounding-box edge")
	}
}

func TestSparklineDoesNotBridgeMissingSamplesOrTimeGaps(t *testing.T) {
	cases := map[string][]nativeSample{
		"missing": {{ts: 1, values: [5]float64{0}}, {ts: 2, values: [5]float64{math.NaN()}}, {ts: 3, values: [5]float64{100}}},
		"gap":     {{ts: 1, values: [5]float64{0}}, {ts: 2, values: [5]float64{0}}, {ts: 5, values: [5]float64{100}}, {ts: 6, values: [5]float64{100}}},
	}
	for name, samples := range cases {
		t.Run(name, func(t *testing.T) {
			a := &nativeUI{history: samples}
			tt := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.sparkline(c, 0, ui.RGB(255, 0, 0)) }) }, 100, 46)
			img := tt.Image()
			for y := 0; y < 46; y++ {
				for x := 45; x <= 55; x++ {
					p := img.RGBAAt(x, y)
					if p.R > 150 && p.G < 100 && p.B < 100 {
						t.Fatalf("line bridges missing interval at %d,%d", x, y)
					}
				}
			}
		})
	}
}
