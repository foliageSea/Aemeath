//go:build cgo && ((darwin && (amd64 || arm64)) || (linux && (amd64 || arm64 || arm)) || (windows && (amd64 || arm64 || 386)))

package wake

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in uses already-installed real models, never downloads or uses a mic.
func TestNativeSmoke(t *testing.T) {
	if os.Getenv("AEMEATH_WAKE_SMOKE") != "1" {
		t.Skip("set AEMEATH_WAKE_SMOKE=1 after explicit asset setup")
	}
	dir := os.Getenv("AEMEATH_WAKE_MODEL_DIR")
	if dir == "" {
		dir = DefaultModelDir()
	}
	d, err := New(Config{ModelDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v, err := NewVAD(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	silence := make([]int16, 320)
	for i := 0; i < 1600; i++ {
		hit, err := d.Feed(silence)
		if err != nil || hit {
			t.Fatalf("silence KWS hit=%v err=%v", hit, err)
		}
		speech, err := v.Speech(silence)
		if err != nil || speech {
			t.Fatalf("silence VAD speech=%v err=%v", speech, err)
		}
	}
	if d.base == 0 || len(d.recent) > 3*SampleRate || len(v.pending) >= 512 {
		t.Fatal("stream state was not bounded")
	}
	d.Reset()
	v.Reset()
	if d.total != 0 || d.KeywordEndSamples() != 0 || len(d.recent) != 0 {
		t.Fatal("reset failed")
	}
	// Reference is PCM24 mono48k: decimate only for offline model smoke.
	b, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "reference.wav"))
	if err != nil {
		t.Fatal(err)
	}
	var audio []byte
	for off := 12; off+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[off+4:]))
		if off+8+n > len(b) {
			t.Fatal("bad reference WAV")
		}
		if string(b[off:off+4]) == "data" {
			audio = b[off+8 : off+8+n]
			break
		}
		off += 8 + n + (n & 1)
	}
	if len(audio) == 0 {
		t.Fatal("no reference PCM")
	}
	pcm := make([]int16, 0, len(audio)/9)
	for off := 0; off+2 < len(audio); off += 9 {
		value := int32(audio[off]) | int32(audio[off+1])<<8 | int32(audio[off+2])<<16
		if value&0x800000 != 0 {
			value |= ^int32(0xffffff)
		}
		pcm = append(pcm, int16(value>>8))
	}
	speechSeen := false
	for off := 0; off < len(pcm); off += 320 {
		end := off + 320
		if end > len(pcm) {
			end = len(pcm)
		}
		speech, err := v.Speech(pcm[off:end])
		if err != nil {
			t.Fatal(err)
		}
		speechSeen = speechSeen || speech
		if _, err = d.Feed(pcm[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	if !speechSeen {
		t.Fatal("Silero failed to detect real reference speech")
	}
	if _, err = d.Feed(make([]int16, MaxFeedSamples+1)); err == nil {
		t.Fatal("accepted unbounded feed")
	}
	d.Close()
	d.Close()
	v.Close()
	v.Close()
	d.Reset()
	v.Reset()
	if _, err = d.Feed(silence); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err = v.Speech(silence); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	t.Logf("official binding %s, %s, Silero real speech and32s silence passed", BindingVersion, ModelVersion)
}
