package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"time"

	"aemeath/agent/internal/background"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	xdraw "golang.org/x/image/draw"
)

type nativeBackgroundService interface {
	// Synchronously bind the displayed target before starting asynchronous work.
	BackgroundClient(expectedTarget string) (background.Client, error)
}

type nativeBackgroundOps struct {
	pick   func() (string, error)
	load   func(string) (image.Image, error)
	render func(image.Image, background.Edit) (*background.Result, error)
}

type nativeBackgroundState struct {
	target                 string
	client                 background.Client
	connected              bool
	checked                bool
	info                   background.Info
	deviceError            string
	request                uint64
	cancel                 context.CancelFunc
	action                 string
	clearConfirm           bool
	loadConfirm            bool
	source                 image.Image
	sourceBitmap           *ui.Bitmap
	sourceThumb            image.Rectangle
	sourceName             string
	sourceBytes            int64
	edit                   background.Edit
	editRevision           uint64
	renderedRevision       uint64
	result                 *background.Result
	preview                *ui.Bitmap
	previewError           string
	previewPending         bool
	previewDue             time.Time
	rendering              bool
	loading                bool
	dragging               bool
	dragStartX, dragStartY float32
	dragEdit               background.Edit
}

// Device state belongs to one connection; the local edit is deliberately independent.
func (a *nativeUI) syncBackgroundConnection() {
	b := &a.background
	if b.target == a.snapshot.Target && b.connected == a.snapshot.Connected && (b.client == nil || b.client.Current()) {
		return
	}
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	b.target, b.connected = a.snapshot.Target, a.snapshot.Connected
	b.request++
	b.client = nil
	b.checked, b.info, b.deviceError = false, background.Info{}, ""
	b.action, b.clearConfirm, b.loadConfirm = "", false, false
}

func nativePickBackground() (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: mygo.FocusedWindow(), Title: "选择监控台背景", ButtonLabel: "选择图片",
		Filters: []mygo.FileFilter{{Name: "背景图片", Extensions: []string{"jpg", "jpeg", "png", "webp"}}},
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// The full source is kept for lossless re-editing; only a bounded thumbnail enters the GPU.
func nativeBackgroundThumbnail(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	scale := math.Min(1, 1024/float64(max(bounds.Dx(), bounds.Dy())))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(bounds.Dx())*scale)), max(1, int(float64(bounds.Dy())*scale))))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, bounds, draw.Src, nil)
	return dst
}

func (a *nativeUI) chooseBackground() {
	b := &a.background
	if b.loading || b.action != "" || a.update == nil {
		return
	}
	pick, load := a.backgroundOps.pick, a.backgroundOps.load
	if pick == nil {
		pick = nativePickBackground
	}
	if load == nil {
		load = background.Load
	}
	b.loading = true
	dispatch := a.update
	go func() {
		path, err := pick()
		var src image.Image
		var thumb *image.RGBA
		var size int64
		if err == nil && path != "" {
			src, err = load(path)
			if err == nil {
				thumb = nativeBackgroundThumbnail(src)
				if stat, statErr := os.Stat(path); statErr == nil {
					size = stat.Size()
				}
			}
		}
		dispatch(func() {
			b.loading = false
			if err != nil {
				a.notify(nativeNoticeError, "无法载入背景图片："+err.Error())
				return
			}
			if path == "" {
				return
			}
			a.setBackgroundSource(src, thumb, filepath.Base(path), size)
		})
	}()
}

func (a *nativeUI) setBackgroundSource(src image.Image, thumb *image.RGBA, name string, size int64) {
	b := &a.background
	b.source, b.sourceBitmap, b.sourceThumb = src, ui.NewBitmap(thumb), thumb.Bounds()
	b.sourceName, b.sourceBytes = name, size
	b.edit, b.dragging = background.DefaultEdit(), false
	b.result, b.preview = nil, nil
	a.queueBackgroundPreview(time.Time{})
}

func (a *nativeUI) queueBackgroundPreview(now time.Time) {
	b := &a.background
	b.editRevision++
	b.previewPending, b.previewError = true, ""
	b.previewDue = now
	if !now.IsZero() {
		b.previewDue = now.Add(100 * time.Millisecond)
	}
}

// Context.After schedules the debounce on the UI clock: no goroutine per slider event.
// At most one render runs. Changes arriving during it are coalesced into the next one.
func (a *nativeUI) backgroundPreviewTick(c *ui.Context) {
	b := &a.background
	if !b.previewPending || b.source == nil || b.rendering || a.update == nil {
		return
	}
	if delay := b.previewDue.Sub(c.Now()); delay > 0 {
		c.After(delay)
		return
	}
	render := a.backgroundOps.render
	if render == nil {
		render = background.Render
	}
	src, edit, revision, dispatch := b.source, b.edit, b.editRevision, a.update
	b.rendering, b.previewPending = true, false
	go func() {
		result, err := render(src, edit)
		dispatch(func() {
			b.rendering = false
			if b.editRevision != revision {
				return
			}
			if err != nil {
				b.previewError = err.Error()
				b.result = nil
				return
			}
			b.result, b.preview, b.renderedRevision = result, ui.NewBitmap(result.Image), revision
		})
	}()
}

func (a *nativeUI) backgroundCanApply() bool {
	b := &a.background
	_, supported := a.service.(nativeBackgroundService)
	return supported && a.snapshot.Connected && b.connected && b.checked && b.info.Supported && b.deviceError == "" &&
		b.action == "" && !b.loading && !b.rendering && !b.previewPending && b.result != nil && b.renderedRevision == b.editRevision
}

// All network and image decoding is outside the UI thread. A generation and the
// actual live target guard callbacks, including an old response after disconnect.
func (a *nativeUI) backgroundRequest(action string) {
	service, ok := a.service.(nativeBackgroundService)
	if !ok || a.update == nil {
		return
	}
	a.snapshot = a.service.Snapshot()
	a.syncBackgroundConnection()
	b := &a.background
	if !b.connected || b.action != "" {
		return
	}
	if action == "upload" && !a.backgroundCanApply() {
		return
	}
	if action == "remove" && (!b.checked || !b.info.Supported || (!b.info.Present && b.info.Error == "") || b.deviceError != "") {
		return
	}
	if action == "image" && (!b.checked || !b.info.Supported || !b.info.Present || b.loading) {
		return
	}
	// Binding is synchronous: admission later checks this exact connection, not
	// whichever device happens to be current when the goroutine is scheduled.
	client, bindErr := service.BackgroundClient(b.target)
	if bindErr != nil {
		b.checked, b.deviceError = true, bindErr.Error()
		return
	}
	b.client = client
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	b.cancel, b.action, b.dragging = cancel, action, false
	b.request++
	request, target, revision := b.request, b.target, b.info.Revision
	editRevision := b.editRevision
	var data []byte
	if action == "upload" {
		data = b.result.JPEG
	}
	dispatch := a.update
	go func() {
		defer cancel()
		var info background.Info
		var src image.Image
		var thumb *image.RGBA
		var size int64
		var err error
		reconciled := false
		switch action {
		case "upload":
			info, err = client.SetBackground(ctx, data, revision)
		case "remove":
			info, err = client.ClearBackground(ctx, revision)
		case "image":
			var imageData []byte
			// Re-query before/after reading: another client may change the device meanwhile.
			info, err = client.GetBackground(ctx)
			if err == nil && !info.Present {
				err = fmt.Errorf("设备背景已被移除，请刷新状态")
			}
			if err == nil {
				imageData, err = client.GetBackgroundImage(ctx)
			}
			if err == nil {
				sum := sha256.Sum256(imageData)
				if fmt.Sprintf("%x", sum) != info.SHA256 {
					err = fmt.Errorf("设备背景已变化，请重试载入")
				}
			}
			if err == nil {
				src, err = jpeg.Decode(bytes.NewReader(imageData))
				if err == nil {
					thumb = nativeBackgroundThumbnail(src)
					size = int64(len(imageData))
				}
			}
		default:
			info, err = client.GetBackground(ctx)
		}
		// A timeout is not evidence of a failed commit. Read, never retry the write.
		var checkErr error
		if err != nil && (action == "upload" || action == "remove") {
			checkCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			info, checkErr = client.GetBackground(checkCtx)
			stop()
			if checkErr == nil {
				sum := sha256.Sum256(data)
				reconciled = action == "upload" && info.Present && info.Displayed && info.SHA256 == fmt.Sprintf("%x", sum)
				reconciled = reconciled || action == "remove" && !info.Present && !info.Displayed && info.Error == ""
				if reconciled {
					err = nil
				}
			}
		}
		dispatch(func() {
			live := a.service.Snapshot()
			if request != b.request {
				return
			}
			if !client.Current() || !live.Connected || live.Target != target {
				a.snapshot = live
				a.syncBackgroundConnection()
				return
			}
			b.action, b.cancel, b.checked = "", nil, true
			if err != nil {
				if (action == "upload" || action == "remove") && checkErr == nil {
					b.info = info
					b.deviceError = ""
					a.notify(nativeNoticeWarning, "背景操作未确认完成，已刷新设备状态；编辑已保留："+err.Error())
				} else {
					b.deviceError = err.Error()
					a.notify(nativeNoticeError, "背景操作未完成，编辑已保留："+err.Error())
				}
				return
			}
			b.info, b.deviceError = info, ""
			switch action {
			case "image":
				if editRevision == b.editRevision && !b.loading {
					a.setBackgroundSource(src, thumb, "设备当前背景（已压缩）", size)
				}
			case "upload":
				a.notify(nativeNoticeSuccess, "背景已保存并应用到监控台。")
			case "remove":
				a.notify(nativeNoticeSuccess, "背景已移除，监控台恢复纯色底。")
			}
		})
	}()
}

func (a *nativeUI) backgroundSettingsView(c *ui.Context) {
	a.syncBackgroundConnection()
	b := &a.background
	if b.edit.Quality == 0 {
		b.edit = background.DefaultEdit()
	}
	_, supported := a.service.(nativeBackgroundService)
	if supported && b.connected && !b.checked && b.action == "" {
		a.backgroundRequest("status")
	}
	a.backgroundPreviewTick(c)
	nativeSettingsSection(c, "background-card", "监控台背景", func() {
		a.backgroundStatusView(c, supported)
		nativeSettingsActions(c, "background-source-actions", func() {
			label := "选择图片"
			if b.loading {
				label = "正在载入…"
			}
			ui.Button(c.Key("background-pick"), label).Label("选择背景图片").Disabled(b.loading || b.action != "").OnClick(a.chooseBackground)
			ui.Button(c.Key("background-device-image"), "从设备载入").Disabled(!supported || !b.connected || !b.checked || !b.info.Supported || !b.info.Present || b.action != "" || b.loading || b.deviceError != "").OnClick(func() {
				if b.source != nil {
					b.loadConfirm = true
				} else {
					a.backgroundRequest("image")
				}
			})
			ui.Button(c.Key("background-refresh"), "刷新状态").Disabled(!supported || !b.connected || b.action != "").OnClick(func() { a.backgroundRequest("status") })
		})
		if b.source != nil {
			a.backgroundEditorView(c)
		} else {
			ui.Box(c.Key("background-empty")).FillWidth().Height(96).Radius(10).Background(ui.Hex("#0B0F14")).Center().Children(func() {
				ui.Text(c, "未选择图片").FontSize(12).TextColor(ui.Hex("#A7AFBA"))
			})
		}
		nativeSettingsActions(c, "background-apply-actions", func() {
			ui.PrimaryButton(c.Key("background-apply"), "上传并应用").Disabled(!a.backgroundCanApply()).OnClick(func() { a.backgroundRequest("upload") })
			ui.Button(c.Key("background-remove"), "移除背景").Disabled(!supported || !b.connected || !b.checked || !b.info.Supported || (!b.info.Present && b.info.Error == "") || b.action != "" || b.deviceError != "").OnClick(func() { b.clearConfirm = true })
		})
		if ui.AlertDialog(c, &b.clearConfirm, "移除监控台背景？", "设备将恢复纯色背景。", "取消", "移除") == 1 {
			a.backgroundRequest("remove")
		}
		if ui.AlertDialog(c, &b.loadConfirm, "载入设备背景？", "当前本地编辑将被替换。", "取消", "载入") == 1 {
			a.backgroundRequest("image")
		}
	})
}

func (a *nativeUI) backgroundStatusView(c *ui.Context, supported bool) {
	b := &a.background
	status, color := "设备未连接", c.Theme().TextMuted
	switch {
	case !supported:
		status = "当前服务不支持背景上传，请更新桌面应用。"
	case !b.connected:
	case b.action != "":
		status = map[string]string{"status": "正在读取背景状态…", "upload": "正在上传并保存背景…", "remove": "正在移除背景…", "image": "正在载入设备背景…"}[b.action]
	case b.deviceError != "":
		status, color = b.deviceError, c.Theme().Warning
	case b.checked && !b.info.Supported:
		status, color = "设备暂不支持背景上传，请升级固件和分区表。", c.Theme().Warning
	case b.checked && !b.info.Present && b.info.Error != "":
		status, color = "背景状态异常，清屏未确认；可重试移除或重启设备："+b.info.Error, c.Theme().Warning
	case b.checked && b.info.Present:
		status = fmt.Sprintf("设备已保存背景 · %d × %d · %.1f KiB", b.info.Width, b.info.Height, float64(b.info.Bytes)/1024)
		if !b.info.Displayed {
			status, color = "背景已保存但未能显示："+b.info.Error, c.Theme().Warning
		}
	case b.checked:
		status = "设备当前无背景图"
	}
	ui.Text(c.Key("background-status"), status).FontSize(12).TextColor(color).Wrap()
}
