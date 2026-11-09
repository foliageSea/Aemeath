//go:build cgo && ((darwin && (amd64 || arm64)) || (linux && (amd64 || arm64 || arm)) || (windows && (amd64 || arm64 || 386)))

package wake

/*
// Opaque public C API declarations from sherpa-onnx v2.0.1. The official Go
// wrapper omits timestamps; this bridge reads the public JSON result instead.
const char *SherpaOnnxGetKeywordResultAsJson(const void *spotter, const void *stream);
void SherpaOnnxFreeKeywordResultJson(const char *s);
*/
import "C"
import (
	"encoding/json"
	"fmt"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/v2/sherpa_onnx"
	"reflect"
	"runtime"
	"unsafe"
)

type timedResult struct {
	Keyword    string    `json:"keyword"`
	StartTime  float64   `json:"start_time"`
	Timestamps []float64 `json:"timestamps"`
}

// The pinned official Go wrappers hold exactly one opaque C pointer. Guard
// that layout before accessing it; a binding upgrade must review this bridge.
func nativePointer(v any) unsafe.Pointer {
	r := reflect.ValueOf(v)
	if r.IsNil() {
		return nil
	}
	t := r.Elem().Type()
	if t.NumField() != 1 || t.Field(0).Name != "impl" || t.Field(0).Offset != 0 || t.Size() != unsafe.Sizeof(uintptr(0)) {
		panic("unsupported sherpa-onnx Go wrapper ABI; expected v2.0.1")
	}
	return r.Elem().Field(0).UnsafePointer()
}
func validNativeStream(s *sherpa.OnlineStream) bool { return s != nil && nativePointer(s) != nil }
func keywordResult(s *sherpa.KeywordSpotter, stream *sherpa.OnlineStream) (timedResult, error) {
	p := C.SherpaOnnxGetKeywordResultAsJson(nativePointer(s), nativePointer(stream))
	runtime.KeepAlive(s)
	runtime.KeepAlive(stream)
	if p == nil {
		return timedResult{}, fmt.Errorf("sherpa keyword result unavailable")
	}
	defer C.SherpaOnnxFreeKeywordResultJson(p)
	raw := C.GoString(p)
	if len(raw) > 8192 {
		return timedResult{}, fmt.Errorf("keyword result exceeds limit")
	}
	var r timedResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, fmt.Errorf("invalid keyword result: %w", err)
	}
	return r, nil
}
