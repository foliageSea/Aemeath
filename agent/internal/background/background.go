// Package background prepares bounded, static images for the 480-square display.
// Keep the loaded source for a whole edit session and render from it each time.
package background

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"reflect"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	Size           = 480
	MaxBytes       = 196608
	MaxSourceBytes = 20 << 20
	MaxPixels      = 32_000_000
	MaxDimension   = 16384
)

// Client is a reusable handle bound to a single device connection. Obtain it
// synchronously in an action handler, before starting asynchronous UI work.
type Client interface {
	// Current is a synchronous, I/O-free check of the captured connection identity.
	Current() bool
	GetBackground(context.Context) (Info, error)
	GetBackgroundImage(context.Context) ([]byte, error)
	SetBackground(context.Context, []byte, uint64) (Info, error)
	ClearBackground(context.Context, uint64) (Info, error)
}

type Edit struct {
	Zoom, X, Y, Brightness, Contrast float64
	Quality                          int
}

func DefaultEdit() Edit {
	return Edit{Zoom: 1, X: .5, Y: .5, Brightness: 100, Contrast: 100, Quality: 85}
}

type Result struct {
	// Image is decoded from JPEG, not the pre-compression render.
	Image   *image.RGBA
	JPEG    []byte
	Quality int
}

type Info struct {
	V         int    `json:"v"`
	Supported bool   `json:"supported"`
	Present   bool   `json:"present"`
	Displayed bool   `json:"displayed"`
	Revision  uint64 `json:"revision"`
	SHA256    string `json:"sha256"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Bytes     int    `json:"bytes"`
	MaxBytes  int    `json:"maxBytes"`
	Scope     string `json:"scope"`
	Error     string `json:"error,omitempty"`
}

func dimensions(w, h int) error {
	if w <= 0 || h <= 0 || w > MaxDimension || h > MaxDimension || int64(w)*int64(h) > MaxPixels {
		return fmt.Errorf("图片尺寸无效：最多 3200 万像素、单边 %d，当前 %d×%d", MaxDimension, w, h)
	}
	return nil
}

// Load validates before decoding and retains the full oriented source pixels.
// It does not resize, flatten transparency, or modify the selected source file.
func Load(path string) (image.Image, error) {
	// Stat before Open also avoids blocking on FIFOs/devices selected as files.
	initial, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("读取背景图片信息：%w", err)
	}
	if !initial.Mode().IsRegular() {
		return nil, fmt.Errorf("背景图片必须是普通文件")
	}
	if initial.Size() > MaxSourceBytes {
		return nil, fmt.Errorf("背景原图不能超过 20 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开背景图片：%w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取背景图片信息：%w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("背景图片必须是普通文件")
	}
	if st.Size() > MaxSourceBytes {
		return nil, fmt.Errorf("背景原图不能超过 20 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取背景图片：%w", err)
	}
	if len(data) > MaxSourceBytes {
		return nil, fmt.Errorf("背景原图不能超过 20 MiB")
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		if err := staticWebP(data); err != nil {
			return nil, err
		}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("无法识别图片，请选择 JPEG、PNG 或静态 WebP：%w", err)
	}
	if format != "jpeg" && format != "png" && format != "webp" {
		return nil, fmt.Errorf("不支持 %s 图片，仅支持 JPEG、PNG、静态 WebP", format)
	}
	if err := dimensions(cfg.Width, cfg.Height); err != nil {
		return nil, err
	}
	if format == "webp" {
		if err := staticWebP(data); err != nil {
			return nil, err
		}
	}
	orientation := 1
	if format == "jpeg" {
		orientation, err = jpegOrientation(data)
		if err != nil {
			return nil, fmt.Errorf("JPEG 方向信息无效：%w", err)
		}
	}
	src, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解码背景图片：%w", err)
	}
	if decodedFormat != format || src.Bounds().Dx() != cfg.Width || src.Bounds().Dy() != cfg.Height {
		return nil, fmt.Errorf("图片头与解码尺寸不一致")
	}
	return orient(src, orientation), nil
}

func staticWebP(data []byte) error {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return fmt.Errorf("WebP 文件结构无效")
	}
	for p := 12; p < len(data); {
		if len(data)-p < 8 {
			return fmt.Errorf("WebP 数据截断")
		}
		tag := string(data[p : p+4])
		n := uint64(binary.LittleEndian.Uint32(data[p+4 : p+8]))
		start := p + 8
		if n > uint64(len(data)-start) {
			return fmt.Errorf("WebP 数据截断")
		}
		if tag == "ANIM" || tag == "ANMF" || (tag == "VP8X" && n > 0 && data[start]&2 != 0) {
			return fmt.Errorf("不支持动态 WebP，请选择静态图片")
		}
		next := uint64(start) + n + (n & 1)
		if next > uint64(len(data)) {
			return fmt.Errorf("WebP 数据截断")
		}
		p = int(next)
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func clamp(v, low, high, fallback float64) float64 {
	if !finite(v) {
		return fallback
	}
	return math.Max(low, math.Min(high, v))
}

// CropRect never panics for invalid edit values. Render, unlike this geometry
// helper, rejects them. X/Y describe the top-left's ratio of its movable range.
func CropRect(bounds image.Rectangle, e Edit) image.Rectangle {
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || w > MaxDimension || h > MaxDimension {
		return image.Rectangle{Min: bounds.Min, Max: bounds.Min}
	}
	zoom := clamp(e.Zoom, 1, 4, 1)
	side := max(1, int(math.Floor(float64(min(w, h))/zoom)))
	x := bounds.Min.X + int(math.Round(float64(w-side)*clamp(e.X, 0, 1, .5)))
	y := bounds.Min.Y + int(math.Round(float64(h-side)*clamp(e.Y, 0, 1, .5)))
	return image.Rect(x, y, x+side, y+side)
}

func validateEdit(e Edit) error {
	for _, v := range []struct {
		name             string
		value, low, high float64
	}{
		{"缩放", e.Zoom, 1, 4}, {"水平位置", e.X, 0, 1}, {"垂直位置", e.Y, 0, 1},
		{"图片亮度", e.Brightness, 0, 200}, {"对比度", e.Contrast, 0, 200},
	} {
		if !finite(v.value) || v.value < v.low || v.value > v.high {
			return fmt.Errorf("%s必须为 %.0f–%.0f 范围内的有限数值", v.name, v.low, v.high)
		}
	}
	if e.Quality < 40 || e.Quality > 95 {
		return fmt.Errorf("压缩质量必须为 40–95")
	}
	return nil
}

func Render(src image.Image, e Edit) (*Result, error) {
	if src == nil || (reflect.ValueOf(src).Kind() == reflect.Pointer && reflect.ValueOf(src).IsNil()) {
		return nil, fmt.Errorf("请先选择背景图片")
	}
	if err := validateEdit(e); err != nil {
		return nil, err
	}
	bounds := src.Bounds()
	if err := dimensions(bounds.Dx(), bounds.Dy()); err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, Size, Size))
	xdraw.CatmullRom.Scale(out, out.Bounds(), src, CropRect(bounds, e), draw.Src, nil)
	var tone [256]uint8
	for i := range tone {
		tone[i] = uint8(math.Round(math.Max(0, math.Min(255, ((float64(i)-127.5)*e.Contrast/100+127.5)*e.Brightness/100))))
	}
	for i := 0; i < len(out.Pix); i += 4 {
		a := uint32(255 - out.Pix[i+3])
		out.Pix[i] = tone[min(255, int(out.Pix[i])+int((11*a+127)/255))]
		out.Pix[i+1] = tone[min(255, int(out.Pix[i+1])+int((15*a+127)/255))]
		out.Pix[i+2] = tone[min(255, int(out.Pix[i+2])+int((20*a+127)/255))]
		out.Pix[i+3] = 255
	}
	for quality := e.Quality; ; quality = max(40, quality-5) {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, out, &jpeg.Options{Quality: quality}); err != nil {
			return nil, fmt.Errorf("压缩背景图片：%w", err)
		}
		if b.Len() <= MaxBytes {
			decoded, err := jpeg.Decode(bytes.NewReader(b.Bytes()))
			if err != nil {
				return nil, fmt.Errorf("生成压缩预览：%w", err)
			}
			preview := image.NewRGBA(out.Bounds())
			draw.Draw(preview, preview.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
			return &Result{Image: preview, JPEG: b.Bytes(), Quality: quality}, nil
		}
		if quality == 40 {
			return nil, fmt.Errorf("压缩后仍超过 192 KiB，请选择其他图片")
		}
	}
}

// ValidateJPEG validates the exact wire format before upload or preview decode.
// The byte, dimension, coding-mode and decoded-image limits are all checked.
func ValidateJPEG(data []byte) error {
	if len(data) == 0 || len(data) > MaxBytes {
		return fmt.Errorf("背景 JPEG 大小必须为 1–%d 字节", MaxBytes)
	}
	if len(data) < 4 || data[0] != 255 || data[1] != 216 || data[len(data)-2] != 255 || data[len(data)-1] != 217 {
		return fmt.Errorf("背景数据不是完整的 JPEG")
	}
	baseline := false
	err := jpegSegments(data, func(marker byte, payload []byte) error {
		if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
			if marker != 0xc0 || baseline {
				return fmt.Errorf("背景仅支持 baseline JPEG")
			}
			if len(payload) < 6 || payload[0] != 8 || (payload[5] != 1 && payload[5] != 3) {
				return fmt.Errorf("JPEG 颜色格式无效")
			}
			baseline = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !baseline {
		return fmt.Errorf("背景仅支持 baseline JPEG")
	}
	if err := jpegComplete(data); err != nil {
		return err
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != Size || cfg.Height != Size {
		return fmt.Errorf("背景 JPEG 必须为 480×480")
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || decoded.Bounds().Dx() != Size || decoded.Bounds().Dy() != Size {
		return fmt.Errorf("背景 JPEG 解码失败")
	}
	return nil
}

// jpegComplete locates the actual EOI through byte-stuffed entropy data. The
// standard JPEG decoder accepts trailing bytes; the wire format does not.
func jpegComplete(data []byte) error {
	scanned := false
	for p := 2; p < len(data); {
		if data[p] != 255 {
			return fmt.Errorf("JPEG 标记无效")
		}
		for p < len(data) && data[p] == 255 {
			p++
		}
		if p >= len(data) {
			return fmt.Errorf("JPEG 数据截断")
		}
		marker := data[p]
		p++
		if marker == 0xd9 {
			if !scanned || p != len(data) {
				return fmt.Errorf("JPEG 包含多余数据或缺少扫描")
			}
			return nil
		}
		if marker == 0xd8 || marker == 0 || marker >= 0xd0 && marker <= 0xd7 {
			return fmt.Errorf("JPEG 标记无效")
		}
		if marker == 1 {
			continue
		}
		if len(data)-p < 2 {
			return fmt.Errorf("JPEG 分段截断")
		}
		n := int(binary.BigEndian.Uint16(data[p : p+2]))
		if n < 2 || n > len(data)-p {
			return fmt.Errorf("JPEG 分段长度无效")
		}
		p += n
		if marker != 0xda {
			continue
		}
		scanned = true
		for p < len(data) {
			if data[p] != 255 {
				p++
				continue
			}
			start := p
			for p < len(data) && data[p] == 255 {
				p++
			}
			if p >= len(data) {
				return fmt.Errorf("JPEG 数据截断")
			}
			if data[p] == 0 || data[p] >= 0xd0 && data[p] <= 0xd7 {
				p++
				continue
			}
			p = start
			break
		}
	}
	return fmt.Errorf("JPEG 结束标记缺失")
}

// jpegSegments visits length-prefixed header segments, stopping at scan data.
func jpegSegments(data []byte, visit func(byte, []byte) error) error {
	if len(data) < 2 || data[0] != 255 || data[1] != 216 {
		return fmt.Errorf("JPEG 文件头无效")
	}
	for p := 2; p < len(data); {
		if data[p] != 255 {
			return fmt.Errorf("JPEG 标记无效")
		}
		for p < len(data) && data[p] == 255 {
			p++
		}
		if p >= len(data) {
			return fmt.Errorf("JPEG 标记截断")
		}
		marker := data[p]
		p++
		if marker == 0xda || marker == 0xd9 {
			return nil
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if len(data)-p < 2 {
			return fmt.Errorf("JPEG 分段截断")
		}
		n := int(binary.BigEndian.Uint16(data[p : p+2]))
		if n < 2 || n > len(data)-p {
			return fmt.Errorf("JPEG 分段长度无效")
		}
		if err := visit(marker, data[p+2:p+n]); err != nil {
			return err
		}
		p += n
	}
	return fmt.Errorf("JPEG 扫描数据缺失")
}

func jpegOrientation(data []byte) (int, error) {
	orientation := 1
	found := false
	err := jpegSegments(data, func(marker byte, payload []byte) error {
		if marker != 0xe1 || !bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			return nil
		}
		value, ok, err := tiffOrientation(payload[6:])
		if err != nil {
			return err
		}
		if ok {
			if found && orientation != value {
				return fmt.Errorf("EXIF 方向冲突")
			}
			orientation, found = value, true
		}
		return nil
	})
	return orientation, err
}

func tiffOrientation(data []byte) (int, bool, error) {
	invalid := func() (int, bool, error) { return 0, false, fmt.Errorf("EXIF 结构或方向值无效") }
	if len(data) < 8 {
		return invalid()
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return invalid()
	}
	if order.Uint16(data[2:4]) != 42 {
		return invalid()
	}
	pos := uint64(order.Uint32(data[4:8]))
	if pos < 8 || pos+2 > uint64(len(data)) {
		return invalid()
	}
	count := uint64(order.Uint16(data[pos : pos+2]))
	pos += 2
	if count > (uint64(len(data))-pos)/12 {
		return invalid()
	}
	for i := uint64(0); i < count; i++ {
		entry := data[pos+i*12 : pos+(i+1)*12]
		if order.Uint16(entry[:2]) != 0x112 {
			continue
		}
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return invalid()
		}
		value := int(order.Uint16(entry[8:10]))
		if value < 1 || value > 8 {
			return invalid()
		}
		return value, true, nil
	}
	return 1, false, nil
}

func orient(src image.Image, orientation int) image.Image {
	if orientation == 1 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w, h
	if orientation >= 5 {
		ow, oh = h, w
	}
	out := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			sx, sy := x, y
			switch orientation {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			out.SetNRGBA(x, y, color.NRGBAModel.Convert(src.At(b.Min.X+sx, b.Min.Y+sy)).(color.NRGBA))
		}
	}
	return out
}
