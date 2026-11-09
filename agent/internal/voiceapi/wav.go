// Package voiceapi implements MiMo speech requests and bounded PCM WAV conversion.
package voiceapi

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

const (
	sampleRate      = 16000
	maxAudioSeconds = 120
	maxWAVBytes     = 32 << 20
)

// EncodeWAV returns a mono PCM16 little-endian RIFF/WAVE file, or nil for invalid sizes.
func EncodeWAV(pcm []int16, rate int) []byte {
	if rate < 8000 || rate > 192000 || len(pcm) == 0 || len(pcm) > rate*maxAudioSeconds {
		return nil
	}
	b := make([]byte, 44+2*len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(pcm)))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(v))
	}
	return b
}

// ReferencePCM reads a bounded local WAV and returns 16 kHz mono PCM16.
// In particular, the original 48 kHz, 24-bit voice reference needs no external converter.
func ReferencePCM(path string) ([]int16, error) {
	return referencePCM(context.Background(), path)
}

func referencePCM(ctx context.Context, path string) ([]int16, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxWAVBytes {
		return nil, errors.New("voice reference: invalid or oversized WAV file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("voice reference: cannot open WAV file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxWAVBytes {
		return nil, errors.New("voice reference: invalid or oversized WAV file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxWAVBytes+1))
	if err != nil || len(b) > maxWAVBytes {
		return nil, errors.New("voice reference: cannot read bounded WAV file")
	}
	return decodeWAVContext(ctx, b)
}

func decodeWAV(b []byte) ([]int16, error) { return decodeWAVContext(context.Background(), b) }

func decodeWAVContext(ctx context.Context, b []byte) ([]int16, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bad := errors.New("audio: invalid or unsupported WAV (PCM8/16/24/32 or float32 required)")
	if len(b) < 12 || len(b) > maxWAVBytes || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, bad
	}
	end := int64(binary.LittleEndian.Uint32(b[4:8])) + 8
	if end != int64(len(b)) {
		return nil, bad
	}
	var format, channels, bits, align uint16
	var rate uint32
	var data []byte
	haveFmt := false
	for pos := 12; pos < len(b); {
		if len(b)-pos < 8 {
			return nil, bad
		}
		n := int64(binary.LittleEndian.Uint32(b[pos+4:]))
		start := pos + 8
		stop := int64(start) + n
		if stop > int64(len(b)) {
			return nil, bad
		}
		chunk := b[start:int(stop)]
		switch string(b[pos : pos+4]) {
		case "fmt ":
			if haveFmt || len(chunk) < 16 {
				return nil, bad
			}
			haveFmt = true
			format = binary.LittleEndian.Uint16(chunk)
			channels = binary.LittleEndian.Uint16(chunk[2:])
			rate = binary.LittleEndian.Uint32(chunk[4:])
			align = binary.LittleEndian.Uint16(chunk[12:])
			bits = binary.LittleEndian.Uint16(chunk[14:])
			if format == 0xfffe {
				if len(chunk) < 40 || binary.LittleEndian.Uint16(chunk[16:]) < 22 || binary.LittleEndian.Uint16(chunk[18:]) != bits {
					return nil, bad
				}
				// KSDATAFORMAT_SUBTYPE_PCM / IEEE_FLOAT GUID, little endian.
				guidTail := []byte{0, 0, 0, 0, 16, 0, 128, 0, 0, 170, 0, 56, 155, 113}
				for i, v := range guidTail {
					if chunk[26+i] != v {
						return nil, bad
					}
				}
				format = binary.LittleEndian.Uint16(chunk[24:])
			}
			if channels < 1 || channels > 8 || rate < 8000 || rate > 192000 || (bits != 8 && bits != 16 && bits != 24 && bits != 32) || (format != 1 && format != 3) || (format == 3 && bits != 32) || int(align) != int(channels)*int(bits/8) || binary.LittleEndian.Uint32(chunk[8:]) != rate*uint32(align) {
				return nil, bad
			}
		case "data":
			if data != nil {
				return nil, bad
			}
			data = chunk
		}
		next := int(stop) + int(n&1)
		if next > len(b) {
			return nil, bad
		}
		pos = next
	}
	if !haveFmt || len(data) == 0 || len(data)%int(align) != 0 {
		return nil, bad
	}
	frames := len(data) / int(align)
	if frames > int(rate)*maxAudioSeconds {
		return nil, errors.New("audio: WAV exceeds 120 seconds")
	}
	mono := make([]float64, frames)
	width := int(bits / 8)
	for i := range mono {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var sum float64
		for ch := 0; ch < int(channels); ch++ {
			p := data[i*int(align)+ch*width:]
			var v float64
			switch {
			case format == 3:
				v = float64(math.Float32frombits(binary.LittleEndian.Uint32(p)))
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return nil, bad
				}
			case bits == 8:
				v = float64(int(p[0])-128) / 128
			case bits == 16:
				v = float64(int16(binary.LittleEndian.Uint16(p))) / 32768
			case bits == 24:
				x := int32(p[0]) | int32(p[1])<<8 | int32(p[2])<<16
				x = x << 8 >> 8
				v = float64(x) / 8388608
			case bits == 32:
				v = float64(int32(binary.LittleEndian.Uint32(p))) / 2147483648
			}
			sum += v
		}
		mono[i] = sum / float64(channels)
	}
	return resampleContext(ctx, mono, int(rate))
}

// A windowed sinc low pass prevents high-frequency aliases during downsampling.
func resample(src []float64, rate int) []int16 {
	out, _ := resampleContext(context.Background(), src, rate)
	return out
}

func resampleContext(ctx context.Context, src []float64, rate int) ([]int16, error) {
	n := len(src) * sampleRate / rate
	if n < 1 {
		return nil, errors.New("audio: WAV contains less than one output sample")
	}
	out := make([]int16, n)
	cutoff := math.Min(1, float64(sampleRate)/float64(rate))
	const radius = 24
	for i := range out {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		x := float64(i) * float64(rate) / sampleRate
		var v, weight float64
		if rate == sampleRate {
			v = src[i]
		} else {
			center := int(x)
			for j := center - radius + 1; j <= center+radius; j++ {
				if j < 0 || j >= len(src) {
					continue
				}
				d := float64(j) - x
				a := math.Pi * d * cutoff
				sinc := 1.0
				if math.Abs(a) > 1e-10 {
					sinc = math.Sin(a) / a
				}
				w := cutoff * sinc * (0.5 + 0.5*math.Cos(math.Pi*d/radius))
				v += src[j] * w
				weight += w
			}
			if weight != 0 {
				v /= weight
			}
		}
		v = math.Round(v * 32768)
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out, nil
}
