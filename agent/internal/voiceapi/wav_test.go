package voiceapi

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixtureWAV(rate, channels, bits int, samples []int32) []byte {
	width := bits / 8
	size := len(samples) * width
	b := make([]byte, 44+size+(size&1))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*channels*width))
	binary.LittleEndian.PutUint16(b[32:], uint16(channels*width))
	binary.LittleEndian.PutUint16(b[34:], uint16(bits))
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(size))
	for i, x := range samples {
		for j := 0; j < width; j++ {
			b[44+i*width+j] = byte(x >> uint(8*j))
		}
	}
	return b
}
func writeReference(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "reference.wav")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEncodeRoundtrip(t *testing.T) {
	want := []int16{-32768, -123, 0, 123, 32767}
	got, err := decodeWAV(EncodeWAV(want, 16000))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if EncodeWAV(nil, 16000) != nil || EncodeWAV(want, 0) != nil {
		t.Fatal("invalid input encoded")
	}
}
func TestReference24Bit48KAndStereo(t *testing.T) {
	samples := make([]int32, 4800*2)
	for i := 0; i < 4800; i++ {
		samples[2*i] = 3 << 20
		samples[2*i+1] = 1 << 20
	}
	got, err := ReferencePCM(writeReference(t, fixtureWAV(48000, 2, 24, samples)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1600 {
		t.Fatalf("samples=%d", len(got))
	}
	for i, v := range got {
		if v != 8192 {
			t.Fatalf("sample[%d]=%d", i, v)
		}
	}
	// Signed 24-bit values must sign extend, including the most negative value.
	got, err = decodeWAV(fixtureWAV(16000, 1, 24, []int32{-8388608, 8388352, -256, 0}))
	if err != nil || !reflect.DeepEqual(got, []int16{-32768, 32767, -1, 0}) {
		t.Fatalf("signed conversion %v %v", got, err)
	}
}
func TestResamplerFiltersAliases(t *testing.T) {
	tone := func(hz float64) []float64 {
		x := make([]float64, 48000)
		for i := range x {
			x[i] = .5 * math.Sin(2*math.Pi*hz*float64(i)/48000)
		}
		return x
	}
	rms := func(x []int16) float64 {
		var sum float64
		for _, v := range x[100 : len(x)-100] {
			sum += float64(v) * float64(v)
		}
		return math.Sqrt(sum / float64(len(x)-200))
	}
	low := rms(resample(tone(1000), 48000))
	high := rms(resample(tone(12000), 48000))
	if low < 10000 || high > 200 {
		t.Fatalf("passband=%f alias=%f", low, high)
	}
}
func TestWAVValidation(t *testing.T) {
	valid := EncodeWAV([]int16{1, 2, 3}, 16000)
	cases := map[string][]byte{"empty": nil, "truncated": valid[:len(valid)-1], "wrong header": append([]byte(nil), valid...), "bad block": append([]byte(nil), valid...), "oversized chunk": append([]byte(nil), valid...), "unsupported compression": append([]byte(nil), valid...)}
	cases["wrong header"][0] = 'X'
	cases["bad block"][32] = 0
	binary.LittleEndian.PutUint32(cases["oversized chunk"][40:], 0xffffffff)
	cases["unsupported compression"][20] = 6
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeWAV(b); err == nil {
				t.Fatal("accepted invalid WAV")
			}
		})
	}
	// Unknown chunks and mandatory padding are valid and skipped.
	b := append([]byte(nil), valid[:36]...)
	b = append(b, []byte{'J', 'U', 'N', 'K', 1, 0, 0, 0, 7, 0}...)
	b = append(b, valid[36:]...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	if _, err := decodeWAV(b); err != nil {
		t.Fatal(err)
	}
}
func TestFloatAndExtensibleWAV(t *testing.T) {
	b := fixtureWAV(16000, 1, 32, []int32{int32(math.Float32bits(.5)), int32(math.Float32bits(-.5))})
	binary.LittleEndian.PutUint16(b[20:], 3)
	got, err := decodeWAV(b)
	if err != nil || !reflect.DeepEqual(got, []int16{16384, -16384}) {
		t.Fatalf("float %v %v", got, err)
	}
	binary.LittleEndian.PutUint32(b[44:], math.Float32bits(float32(math.NaN())))
	if _, err = decodeWAV(b); err == nil {
		t.Fatal("accepted NaN")
	}
	pcm := fixtureWAV(16000, 1, 24, []int32{256, -256})
	ext := append([]byte(nil), pcm[:36]...)
	ext = append(ext, make([]byte, 24)...)
	ext = append(ext, pcm[36:]...)
	binary.LittleEndian.PutUint32(ext[4:], uint32(len(ext)-8))
	binary.LittleEndian.PutUint32(ext[16:], 40)
	binary.LittleEndian.PutUint16(ext[20:], 0xfffe)
	binary.LittleEndian.PutUint16(ext[36:], 22)
	binary.LittleEndian.PutUint16(ext[38:], 24)
	copy(ext[44:], []byte{1, 0, 0, 0, 0, 0, 16, 0, 128, 0, 0, 170, 0, 56, 155, 113})
	got, err = decodeWAV(ext)
	if err != nil || !reflect.DeepEqual(got, []int16{1, -1}) {
		t.Fatalf("extensible %v %v", got, err)
	}
}
