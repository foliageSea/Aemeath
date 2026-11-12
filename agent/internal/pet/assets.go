// Package pet provides deterministic desktop-pet logic without UI, audio, or network dependencies.
package pet

import (
	_ "embed"
	"image"
	"time"
)

//go:generate go run ../../scripts/pet-assets -source ../../../firmware/main/assets/avatar_source/spritesheet.webp -out assets

// AtlasPNG is the original-size, lossless, alpha-normalized atlas. Treat it as read-only.
//
//go:embed assets/atlas.png
var AtlasPNG []byte

const (
	FrameWidth   = 192
	FrameHeight  = 208
	AtlasColumns = 8
	// FrameDuration is the original default timing; waving uses WaveFrameDuration.
	FrameDuration     = 150 * time.Millisecond
	WaveFrameDuration = 140 * time.Millisecond
)

type Action string

const (
	Idle     Action = "idle"
	RunRight Action = "running-right"
	RunLeft  Action = "running-left"
	Wave     Action = "waving"
	Jump     Action = "jumping"
	Failed   Action = "failed"
	Wait     Action = "waiting"
	Running  Action = "running"
	Review   Action = "review"
)

// Row describes an untrimmed row in the source atlas. Frames never include unused cells.
type Row struct {
	Action        Action
	Row           int
	Frames        int
	FrameDuration time.Duration
}

// Rows is the source manifest in row order. Treat it as read-only.
var Rows = []Row{
	{Idle, 0, 6, FrameDuration}, {RunRight, 1, 8, FrameDuration}, {RunLeft, 2, 8, FrameDuration},
	{Wave, 3, 4, WaveFrameDuration}, {Jump, 4, 5, FrameDuration}, {Failed, 5, 8, FrameDuration},
	{Wait, 6, 6, FrameDuration}, {Running, 7, 6, FrameDuration}, {Review, 8, 6, FrameDuration},
}

// Frame returns the complete 192x208 cell, not a content bounding box.
// Unknown actions and out-of-range indices return an empty rectangle.
func Frame(action Action, index int) image.Rectangle {
	for _, row := range Rows {
		if row.Action == action && index >= 0 && index < row.Frames {
			return image.Rect(index*FrameWidth, row.Row*FrameHeight, (index+1)*FrameWidth, (row.Row+1)*FrameHeight)
		}
	}
	return image.Rectangle{}
}

// ActionFrameDuration preserves the firmware cadence (wave=140ms; others=150ms).
func ActionFrameDuration(action Action) time.Duration {
	for _, r := range Rows {
		if r.Action == action {
			return r.FrameDuration
		}
	}
	return FrameDuration
}

func frameCount(action Action) int {
	for _, r := range Rows {
		if r.Action == action {
			return r.Frames
		}
	}
	return 1
}
