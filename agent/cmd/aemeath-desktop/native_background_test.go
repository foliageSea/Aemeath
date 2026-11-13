package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"aemeath/agent/internal/background"
	"github.com/egoist/mygo/ui"
)

type nativeBackgroundFake struct {
	*nativeFake
	bgMu                sync.Mutex
	info                background.Info
	data                []byte
	puts, deletes, gets int
	writeError          error
	commitOnError       bool
	getGate             <-chan struct{}
	writeGate           <-chan struct{}
	bindings            []string
	clearDisplayError   bool
	connectionSerial    uint64
}

type nativeBoundBackgroundFake struct {
	f                *nativeBackgroundFake
	target           string
	connectionSerial uint64
	writeGate        <-chan struct{}
}

func (f *nativeBackgroundFake) BackgroundClient(expected string) (background.Client, error) {
	state := f.Snapshot()
	if !state.Connected || state.Target != expected {
		return nil, errors.New("connection changed")
	}
	f.bgMu.Lock()
	defer f.bgMu.Unlock()
	f.bindings = append(f.bindings, expected)
	return &nativeBoundBackgroundFake{f: f, target: expected, writeGate: f.writeGate, connectionSerial: f.connectionSerial}, nil
}
func (b *nativeBoundBackgroundFake) Current() bool {
	state := b.f.Snapshot()
	b.f.bgMu.Lock()
	defer b.f.bgMu.Unlock()
	return state.Connected && state.Target == b.target && b.connectionSerial == b.f.connectionSerial
}
func (b *nativeBoundBackgroundFake) check(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !b.Current() {
		return errors.New("connection changed")
	}
	return nil
}
func (b *nativeBoundBackgroundFake) GetBackground(ctx context.Context) (background.Info, error) {
	if err := b.check(ctx); err != nil {
		return background.Info{}, err
	}
	return b.f.GetBackground(ctx)
}
func (b *nativeBoundBackgroundFake) GetBackgroundImage(ctx context.Context) ([]byte, error) {
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	return b.f.GetBackgroundImage(ctx)
}
func (b *nativeBoundBackgroundFake) SetBackground(ctx context.Context, data []byte, revision uint64) (background.Info, error) {
	if b.writeGate != nil {
		select {
		case <-b.writeGate:
		case <-ctx.Done():
			return background.Info{}, ctx.Err()
		}
	}
	if err := b.check(ctx); err != nil {
		return background.Info{}, err
	}
	return b.f.SetBackground(ctx, data, revision)
}
func (b *nativeBoundBackgroundFake) ClearBackground(ctx context.Context, revision uint64) (background.Info, error) {
	if b.writeGate != nil {
		select {
		case <-b.writeGate:
		case <-ctx.Done():
			return background.Info{}, ctx.Err()
		}
	}
	if err := b.check(ctx); err != nil {
		return background.Info{}, err
	}
	return b.f.ClearBackground(ctx, revision)
}

func (f *nativeBackgroundFake) GetBackground(ctx context.Context) (background.Info, error) {
	if f.getGate != nil {
		select {
		case <-f.getGate:
		case <-ctx.Done():
			return background.Info{}, ctx.Err()
		}
	}
	f.bgMu.Lock()
	defer f.bgMu.Unlock()
	f.gets++
	return f.info, nil
}
func (f *nativeBackgroundFake) GetBackgroundImage(context.Context) ([]byte, error) {
	f.bgMu.Lock()
	defer f.bgMu.Unlock()
	return append([]byte(nil), f.data...), nil
}
func (f *nativeBackgroundFake) SetBackground(_ context.Context, data []byte, revision uint64) (background.Info, error) {
	f.bgMu.Lock()
	defer f.bgMu.Unlock()
	f.puts++
	if revision != f.info.Revision {
		return background.Info{}, errors.New("revision conflict")
	}
	if f.writeError == nil || f.commitOnError {
		hash := sha256.Sum256(data)
		f.data = append([]byte(nil), data...)
		f.info.Revision++
		f.info.Present, f.info.Displayed, f.info.Error = true, true, ""
		f.info.SHA256 = fmt.Sprintf("%x", hash)
		f.info.Width, f.info.Height, f.info.Bytes = 480, 480, len(data)
	}
	return f.info, f.writeError
}
func (f *nativeBackgroundFake) ClearBackground(_ context.Context, revision uint64) (background.Info, error) {
	f.bgMu.Lock()
	defer f.bgMu.Unlock()
	f.deletes++
	if revision != f.info.Revision {
		return background.Info{}, errors.New("revision conflict")
	}
	f.info.Revision++
	f.info.Present, f.info.Displayed = false, false
	f.info.SHA256, f.info.Bytes, f.info.Width, f.info.Height = "", 0, 0, 0
	f.data = nil
	if f.clearDisplayError {
		f.info.Error = "display unavailable"
		return f.info, errors.New("display unavailable")
	}
	f.info.Error = ""
	return f.info, nil
}

func nativeBackgroundTest(t *testing.T) (*nativeUI, *nativeBackgroundFake, chan func(), *ui.Tester) {
	t.Helper()
	a, base, updates, tt := nativeTest(t)
	base.state.Connected, base.state.Target = true, "screen.local:80"
	f := &nativeBackgroundFake{nativeFake: base, info: background.Info{V: 1, Supported: true, MaxBytes: background.MaxBytes, Scope: "shared"}}
	a.service = f
	a.refresh()
	a.page, a.settingsTab = "设置", nativeSettingsScreenTab
	tt.SetSize(1100, 1500)
	tt.Frame()
	nativeComplete(t, updates, tt)
	return a, f, updates, tt
}

func nativeBackgroundSource() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(40 + x*150/800), G: uint8(55 + y*100/600), B: uint8(160 - x*70/800), A: 255})
		}
	}
	return img
}
func nativeBackgroundLoad(t *testing.T, a *nativeUI, updates chan func(), tt *ui.Tester) {
	t.Helper()
	src := nativeBackgroundSource()
	a.backgroundOps.pick = func() (string, error) { return "/example/background.png", nil }
	a.backgroundOps.load = func(string) (image.Image, error) { return src, nil }
	a.chooseBackground()
	nativeComplete(t, updates, tt) // Load starts one render on the next frame.
	nativeComplete(t, updates, tt)
	if a.background.result == nil || a.background.rendering {
		t.Fatal("preview did not finish")
	}
}

func TestNativeBackgroundDefaultLocalEditAndApply(t *testing.T) {
	a, f, updates, tt := nativeBackgroundTest(t)
	if !tt.HasText("设备当前无背景图") || a.background.source != nil {
		t.Fatalf("default background was not empty: state=%+v texts=%v", a.background, tt.Texts())
	}
	for _, text := range tt.Texts() {
		for _, annotation := range []string{"三页共享", "上传后保存在设备", "支持 JPEG / PNG", "编辑只影响预览", "可先离线编辑"} {
			if strings.Contains(text, annotation) {
				t.Fatalf("explanatory UI text remains: %s", text)
			}
		}
	}
	if a.backgroundCanApply() {
		t.Fatal("empty editor can upload")
	}
	nativeBackgroundLoad(t, a, updates, tt)
	if !a.backgroundCanApply() || a.dirty || len(f.saves) != 0 || f.puts != 0 {
		t.Fatal("local edit changed settings or device")
	}
	// Exercise the real accessible slider: keyboard updates the draft, never the backlight.
	slider, ok := tt.AccessByRole(ui.RoleSlider, "图片亮度")
	if !ok || slider.Disabled {
		t.Fatal("image brightness slider missing")
	}
	tt.ClickAt(slider.Bounds.X+slider.Bounds.W/2, slider.Bounds.Y+slider.Bounds.H/2)
	tt.Key(0, ui.KeyRight)
	if a.background.edit.Brightness <= 100 {
		t.Fatalf("brightness slider not editable: %v", a.background.edit)
	}
	if len(f.screens) != 0 || f.puts != 0 {
		t.Fatal("image slider sent device/backlight command")
	}
	tt.Advance(110 * time.Millisecond)
	nativeComplete(t, updates, tt)
	a.backgroundRequest("upload")
	a.backgroundRequest("upload") // Duplicate click while pending must not submit twice.
	nativeComplete(t, updates, tt)
	if f.puts != 1 || !a.background.info.Present || !a.background.info.Displayed || a.dirty {
		t.Fatal("upload failed or dirtied unrelated settings")
	}
	if screenshot := os.Getenv("AEMEATH_BACKGROUND_SCREENSHOT"); screenshot != "" {
		file, err := os.Create(screenshot)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, tt.Image())
		closeErr := file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func TestNativeBackgroundDragAndCancellation(t *testing.T) {
	a, f, updates, tt := nativeBackgroundTest(t)
	nativeBackgroundLoad(t, a, updates, tt)
	r, ok := tt.Find("背景裁剪区域")
	if !ok {
		t.Fatal("crop region missing")
	}
	original := a.background.edit
	tt.Press(r.X+120, r.Y+120)
	tt.Move(r.X+160, r.Y+120)
	if a.background.edit.X >= original.X {
		t.Fatal("drag did not move source with pointer")
	}
	tt.CancelPress()
	if a.background.edit != original || a.background.dragging {
		t.Fatal("canceled drag was committed")
	}
	tt.Advance(110 * time.Millisecond)
	nativeComplete(t, updates, tt)
	if f.puts != 0 || f.deletes != 0 {
		t.Fatal("crop wrote device")
	}
}

func TestNativeBackgroundCanceledPickerAndReset(t *testing.T) {
	a, _, updates, tt := nativeBackgroundTest(t)
	nativeBackgroundLoad(t, a, updates, tt)
	src, result := a.background.source, a.background.result
	a.backgroundOps.pick = func() (string, error) { return "", nil }
	a.chooseBackground()
	nativeComplete(t, updates, tt)
	if a.background.source != src || a.background.result != result {
		t.Fatal("canceled picker replaced draft")
	}
	a.background.edit.Brightness, a.background.edit.Zoom = 60, 2
	a.queueBackgroundPreview(tt.Now())
	nativeClick(t, tt, "重置编辑")
	if a.background.edit != background.DefaultEdit() {
		t.Fatal("reset did not reset editing controls")
	}
	tt.Advance(110 * time.Millisecond)
	nativeComplete(t, updates, tt)
}

func TestNativeBackgroundFailureKeepsDraftAndReconciles(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			a, f, updates, tt := nativeBackgroundTest(t)
			nativeBackgroundLoad(t, a, updates, tt)
			src, data := a.background.source, append([]byte(nil), a.background.result.JPEG...)
			f.writeError, f.commitOnError = context.DeadlineExceeded, committed
			a.backgroundRequest("upload")
			nativeComplete(t, updates, tt)
			if f.puts != 1 || a.background.source != src || !bytes.Equal(a.background.result.JPEG, data) {
				t.Fatal("failed write lost draft or retried")
			}
			if a.background.info.Present != committed {
				t.Fatal("actual commit status not reconciled")
			}
			if committed && !strings.Contains(a.notice, "已保存并应用") {
				t.Fatalf("lost acknowledgement reported failure: %s", a.notice)
			}
		})
	}
}

func TestNativeBackgroundRemoveRequiresConfirmationAndKeepsLocalDraft(t *testing.T) {
	a, f, updates, tt := nativeBackgroundTest(t)
	nativeBackgroundLoad(t, a, updates, tt)
	a.backgroundRequest("upload")
	nativeComplete(t, updates, tt)
	src := a.background.source
	nativeClick(t, tt, "移除背景")
	if !tt.HasText("移除监控台背景？") || f.deletes != 0 {
		t.Fatal("remove bypassed confirmation")
	}
	nativeClick(t, tt, "取消")
	if f.deletes != 0 {
		t.Fatal("cancel removed background")
	}
	nativeClick(t, tt, "移除背景")
	nativeClick(t, tt, "移除")
	nativeComplete(t, updates, tt)
	if f.deletes != 1 || a.background.info.Present || a.background.source != src {
		t.Fatal("remove failed or removed local draft")
	}
}

func TestNativeBackgroundLoadDeviceDoesNotOverwriteUnconfirmedDraft(t *testing.T) {
	a, _, updates, tt := nativeBackgroundTest(t)
	nativeBackgroundLoad(t, a, updates, tt)
	a.backgroundRequest("upload")
	nativeComplete(t, updates, tt)
	oldSource := a.background.source
	nativeClick(t, tt, "从设备载入")
	if !tt.HasText("载入设备背景？") || a.background.action != "" {
		t.Fatal("device load replaced draft without confirmation")
	}
	nativeClick(t, tt, "取消")
	if a.background.source != oldSource {
		t.Fatal("cancel discarded draft")
	}
	nativeClick(t, tt, "从设备载入")
	nativeClick(t, tt, "载入")
	nativeComplete(t, updates, tt)
	nativeComplete(t, updates, tt)
	if a.background.sourceName != "设备当前背景（已压缩）" || a.background.source.Bounds().Dx() != 480 {
		t.Fatal("device background was not loaded")
	}
}

func TestNativeBackgroundStaleResultAfterTargetSwitch(t *testing.T) {
	a, f, updates, tt := nativeBackgroundTest(t)
	gate := make(chan struct{})
	f.getGate = gate
	a.backgroundRequest("status")
	f.mu.Lock()
	f.state.Target = "another.local:80"
	f.mu.Unlock()
	a.refresh()
	// An old response may arrive, but cannot restore metadata for the previous target.
	close(gate)
	select {
	case fn := <-updates:
		fn()
	case <-time.After(time.Second):
		t.Fatal("no canceled request completion")
	}
	if a.background.target != "another.local:80" || a.background.checked || a.background.info.Supported {
		t.Fatal("old result leaked into new target")
	}
	// Do not trigger the next target query for this assertion.
	_ = tt
}

func TestNativeBackgroundOfflineAndUnsupported(t *testing.T) {
	a, f, updates, tt := nativeBackgroundTest(t)
	nativeBackgroundLoad(t, a, updates, tt)
	f.mu.Lock()
	f.state.Connected = false
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	if a.backgroundCanApply() {
		t.Fatal("offline apply enabled")
	}
	a.backgroundRequest("upload")
	if f.puts != 0 {
		t.Fatal("offline wrote device")
	}
	f.mu.Lock()
	f.state.Connected = true
	f.mu.Unlock()
	f.bgMu.Lock()
	f.info.Supported = false
	f.bgMu.Unlock()
	a.refresh()
	tt.Frame()
	nativeComplete(t, updates, tt)
	if a.backgroundCanApply() || !tt.HasText("设备暂不支持背景上传，请升级固件和分区表。") {
		t.Fatalf("unsupported firmware not reported: state=%+v texts=%v", a.background, tt.Texts())
	}
}

func TestNativeBackgroundPreviewCoalescesAndRejectsOldResult(t *testing.T) {
	a, _, updates, tt := nativeBackgroundTest(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	a.backgroundOps.render = func(src image.Image, e background.Edit) (*background.Result, error) {
		started <- struct{}{}
		<-release
		return background.Render(src, e)
	}
	src := nativeBackgroundSource()
	a.setBackgroundSource(src, nativeBackgroundThumbnail(src), "source", 0)
	tt.Frame()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("render not started")
	}
	for i := 0; i < 20; i++ {
		a.background.edit.Brightness = float64(50 + i)
		a.queueBackgroundPreview(tt.Now())
		tt.Frame()
	}
	if len(started) != 0 {
		t.Fatal("parallel renders started instead of coalescing")
	}
	// Switch renderer only affects the next request; release the old one.
	a.backgroundOps.render = background.Render
	close(release)
	nativeComplete(t, updates, tt)
	if a.background.result != nil || a.backgroundCanApply() {
		t.Fatal("stale preview accepted")
	}
	tt.Advance(110 * time.Millisecond)
	nativeComplete(t, updates, tt)
	if a.background.result == nil || a.background.renderedRevision != a.background.editRevision {
		t.Fatal("latest coalesced preview missing")
	}
}

func TestNativeBackgroundDeleteCommitWithDisplayErrorNeedsRetry(t *testing.T) {
	a, f, updates, tt := nativeBackgroundTest(t)
	nativeBackgroundLoad(t, a, updates, tt)
	a.backgroundRequest("upload")
	nativeComplete(t, updates, tt)
	f.clearDisplayError = true
	a.backgroundRequest("remove")
	nativeComplete(t, updates, tt)
	if a.background.info.Present || a.background.info.Error == "" {
		t.Fatal("failed clear status not retained")
	}
	if strings.Contains(a.notice, "恢复纯色底") {
		t.Fatal("false clear-display success")
	}
	button, ok := tt.AccessByRole(ui.RoleButton, "移除背景")
	if !ok || button.Disabled {
		t.Fatal("post-commit failed clear cannot be retried")
	}
	if !tt.HasText("背景状态异常，清屏未确认；可重试移除或重启设备：display unavailable") {
		t.Fatal("failed display-clear not explained")
	}
	f.clearDisplayError = false
	a.backgroundRequest("remove")
	nativeComplete(t, updates, tt)
	if f.deletes != 2 || a.background.info.Error != "" || !strings.Contains(a.notice, "恢复纯色底") {
		t.Fatal("retry did not finish clear")
	}
}

func TestNativeBackgroundBindsBeforeAsyncWriteAdmission(t *testing.T) {
	for _, action := range []string{"upload", "remove"} {
		t.Run(action, func(t *testing.T) {
			a, f, updates, tt := nativeBackgroundTest(t)
			nativeBackgroundLoad(t, a, updates, tt)
			if action == "remove" {
				a.backgroundRequest("upload")
				nativeComplete(t, updates, tt)
			}
			gate := make(chan struct{})
			f.writeGate = gate
			bindings, puts, deletes := len(f.bindings), f.puts, f.deletes
			a.backgroundRequest(action)
			if len(f.bindings) != bindings+1 || f.bindings[len(f.bindings)-1] != "screen.local:80" {
				t.Fatal("connection was not bound synchronously in click handler")
			}
			// Change the service without any UI refresh, while the write has not entered.
			f.mu.Lock()
			f.state.Target = "other.local:80"
			f.mu.Unlock()
			close(gate)
			select {
			case fn := <-updates:
				fn()
			case <-time.After(time.Second):
				t.Fatal("missing blocked-write completion")
			}
			if f.puts != puts || f.deletes != deletes {
				t.Fatal("write spilled into the new device")
			}
			a.refresh()
			if a.background.target != "other.local:80" || a.background.checked {
				t.Fatal("old callback changed new device state")
			}
		})
	}
}

func TestNativeBackgroundRejectsCompletedOldConnectionOnSameAddressReconnect(t *testing.T) {
	for _, action := range []string{"status", "image"} {
		t.Run(action, func(t *testing.T) {
			a, f, updates, tt := nativeBackgroundTest(t)
			nativeBackgroundLoad(t, a, updates, tt)
			a.backgroundRequest("upload")
			nativeComplete(t, updates, tt)
			source := a.background.source
			f.bgMu.Lock()
			f.info.Revision = 99
			f.bgMu.Unlock()
			a.backgroundRequest(action)
			// HTTP has completed successfully, but its UI completion has not run yet.
			var complete func()
			select {
			case complete = <-updates:
			case <-time.After(time.Second):
				t.Fatal("no successful request completion")
			}
			f.bgMu.Lock()
			f.connectionSerial++
			f.info = background.Info{V: 1, Supported: true, Revision: 2, MaxBytes: background.MaxBytes, Scope: "shared"}
			f.bgMu.Unlock()
			// Same address + Connected=true, with no sampled offline state or refresh.
			complete()
			if a.background.checked || a.background.info.Revision != 0 || a.background.source != source {
				t.Fatal("completed old connection overwrote current device UI/draft")
			}
			a.refresh()
			tt.Frame()
			nativeComplete(t, updates, tt)
			if !a.background.checked || a.background.info.Revision != 2 || a.background.info.Present {
				t.Fatal("new connection state was not reloaded")
			}
		})
	}
}
