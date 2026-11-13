package main

import (
	"fmt"
	"math"

	"aemeath/agent/internal/background"
	"github.com/egoist/mygo/ui"
)

const nativeBackgroundPreviewSize float32 = 240

func (a *nativeUI) backgroundEditorView(c *ui.Context) {
	b := &a.background
	t := c.Theme()
	ui.Text(c, fmt.Sprintf("%s · %d × %d · %.1f KiB", b.sourceName, b.source.Bounds().Dx(), b.source.Bounds().Dy(), float64(b.sourceBytes)/1024)).FontSize(12).TextColor(t.TextMuted).Wrap()
	ui.Row(c.Key("background-previews")).FillWidth().MinWidth(0).Wrap().Gap(16).AlignItems(ui.Start).Children(func() {
		ui.Column(c.Key("background-crop-column")).Width(nativeBackgroundPreviewSize).Shrink(0).Gap(8).Children(func() {
			ui.Text(c, "裁剪").FontSize(12).TextColor(t.TextMuted)
			pad := ui.Box(c.Key("background-crop")).Label("背景裁剪区域").Size(nativeBackgroundPreviewSize, nativeBackgroundPreviewSize).Shrink(0).Radius(8).Clip().Background(ui.Hex("#0B0F14")).Border(1, t.Border).Cursor(ui.CursorGrab)
			if b.dragging {
				pad.Cursor(ui.CursorGrabbing)
			}
			pad.Disabled(b.loading || b.action != "").HandleInput(a.backgroundCropInput).Draw(func(p *ui.Painter, r ui.Rect) {
				if b.source == nil || b.sourceBitmap == nil {
					return
				}
				bounds := b.source.Bounds()
				crop := background.CropRect(bounds, b.edit)
				sx := float32(b.sourceThumb.Dx()) / float32(bounds.Dx())
				sy := float32(b.sourceThumb.Dy()) / float32(bounds.Dy())
				p.ImageRegion(b.sourceBitmap, ui.Rect{X: float32(crop.Min.X-bounds.Min.X) * sx, Y: float32(crop.Min.Y-bounds.Min.Y) * sy, W: float32(crop.Dx()) * sx, H: float32(crop.Dy()) * sy}, r, ui.ImageOptions{})
				for i := 1; i <= 2; i++ {
					x, y := r.X+r.W*float32(i)/3, r.Y+r.H*float32(i)/3
					p.Line(x, r.Y, x, r.Y+r.H, 1, ui.RGBA(255, 255, 255, 0.4))
					p.Line(r.X, y, r.X+r.W, y, 1, ui.RGBA(255, 255, 255, 0.4))
				}
			})
		})
		ui.Column(c.Key("background-result-column")).Width(nativeBackgroundPreviewSize).Shrink(0).Gap(8).Children(func() {
			ui.Text(c, "预览 · 480 × 480").FontSize(12).TextColor(t.TextMuted)
			ui.Box(c.Key("background-result")).Label("背景压缩预览").Size(nativeBackgroundPreviewSize, nativeBackgroundPreviewSize).Shrink(0).Radius(8).Clip().Background(ui.Hex("#0B0F14")).Border(1, t.Border).Center().Children(func() {
				if b.preview != nil {
					ui.Image(c, b.preview).Fill().Fit(ui.Contain).AccessHidden(true)
				} else {
					ui.Text(c, "正在生成预览…").FontSize(12).TextColor(ui.Hex("#A7AFBA"))
				}
			})
		})
	})
	if b.previewError != "" {
		ui.Text(c, "预览失败："+b.previewError).FontSize(12).TextColor(t.Warning).Wrap()
	} else if b.rendering || b.previewPending {
		ui.Text(c, "正在更新预览…").FontSize(11).TextColor(t.TextMuted)
	} else if b.result != nil {
		text := fmt.Sprintf("输出 %.1f KiB / 192 KiB · 实际质量 %d", float64(len(b.result.JPEG))/1024, b.result.Quality)
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).Wrap()
	}
	disabled := b.loading || b.action != ""
	a.backgroundSlider(c, "background-zoom", "裁剪缩放", &b.edit.Zoom, 1, 4, 0.01, fmt.Sprintf("%.2f ×", b.edit.Zoom), disabled)
	crop := background.CropRect(b.source.Bounds(), b.edit)
	a.backgroundSlider(c, "background-x", "水平位置", &b.edit.X, 0, 1, 0.01, fmt.Sprintf("%.0f%%", b.edit.X*100), disabled || crop.Dx() >= b.source.Bounds().Dx())
	a.backgroundSlider(c, "background-y", "垂直位置", &b.edit.Y, 0, 1, 0.01, fmt.Sprintf("%.0f%%", b.edit.Y*100), disabled || crop.Dy() >= b.source.Bounds().Dy())
	a.backgroundSlider(c, "background-brightness", "图片亮度", &b.edit.Brightness, 0, 200, 1, fmt.Sprintf("%.0f%%", b.edit.Brightness), disabled)
	a.backgroundSlider(c, "background-contrast", "对比度", &b.edit.Contrast, 0, 200, 1, fmt.Sprintf("%.0f%%", b.edit.Contrast), disabled)
	quality := float64(b.edit.Quality)
	if a.backgroundSlider(c, "background-quality", "压缩质量", &quality, 40, 95, 1, fmt.Sprintf("%d", b.edit.Quality), disabled) {
		b.edit.Quality = int(math.Round(quality))
	}
	ui.Button(c.Key("background-reset"), "重置编辑").Disabled(disabled).OnClick(func() {
		b.edit, b.dragging = background.DefaultEdit(), false
		a.queueBackgroundPreview(c.Now())
	})
	if min(crop.Dx(), crop.Dy()) < background.Size {
		ui.Text(c, "裁剪区域小于 480 像素，放大后可能不够清晰。").FontSize(11).TextColor(t.Warning).Wrap()
	}
}

func (a *nativeUI) backgroundSlider(c *ui.Context, key, title string, value *float64, lo, hi, step float64, display string, disabled bool) bool {
	changed := false
	nativeSettingsField(c, key+"-field", title, func() ui.Element {
		var field ui.Element
		ui.Row(c).FillWidth().MinWidth(0).Gap(10).AlignItems(ui.Center).Children(func() {
			field = ui.StepSlider(c.Key(key), value, lo, hi, step).Label(title).Disabled(disabled).Grow(1).MinWidth(0)
			if field.Changed() {
				changed = true
				a.queueBackgroundPreview(c.Now())
			}
			ui.Text(c, display).Width(58).Shrink(0).FontSize(11).TextColor(c.Theme().TextMuted)
		})
		return field
	})
	return changed
}

func backgroundClamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func (a *nativeUI) backgroundCropInput(ev ui.InputEvent) bool {
	b := &a.background
	if b.source == nil || b.loading || b.action != "" {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		b.dragging, b.dragStartX, b.dragStartY, b.dragEdit = true, ev.X, ev.Y, b.edit
		return true
	case ui.InputPointerMove, ui.InputPointerUp:
		if !b.dragging {
			return false
		}
		if ev.Kind == ui.InputPointerUp && ev.Button != 0 && !ev.Canceled {
			return false
		}
		before := b.edit
		if ev.Canceled {
			b.edit = b.dragEdit
		} else {
			crop := background.CropRect(b.source.Bounds(), b.dragEdit)
			// Move the picture with the pointer, rather than moving the crop in that direction.
			k := float64(crop.Dx()) / float64(nativeBackgroundPreviewSize)
			if room := b.source.Bounds().Dx() - crop.Dx(); room > 0 {
				b.edit.X = backgroundClamp(b.dragEdit.X - float64(ev.X-b.dragStartX)*k/float64(room))
			}
			if room := b.source.Bounds().Dy() - crop.Dy(); room > 0 {
				b.edit.Y = backgroundClamp(b.dragEdit.Y - float64(ev.Y-b.dragStartY)*k/float64(room))
			}
		}
		if ev.Kind == ui.InputPointerUp {
			b.dragging = false
		}
		if before != b.edit {
			a.queueBackgroundPreview(ev.Time)
		}
		return true
	}
	return false
}
