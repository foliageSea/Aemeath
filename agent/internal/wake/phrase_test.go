//go:build cgo && ((darwin && (amd64 || arm64)) || (linux && (amd64 || arm64 || arm)) || (windows && (amd64 || arm64 || 386)))

package wake

import (
	"aemeath/agent/internal/voiceapi"
	"os"
	"testing"
)

// Opt-in checks actual keyword inference using explicitly supplied local WAVs.
// It never synthesizes, downloads, records a microphone, or invokes a cloud API.
func TestNativeWakePhrase(t *testing.T) {
	positive := os.Getenv("AEMEATH_WAKE_POSITIVE_WAV")
	if positive == "" {
		t.Skip("set AEMEATH_WAKE_POSITIVE_WAV and AEMEATH_WAKE_NEGATIVE_WAV to local spoken samples")
	}
	dir := os.Getenv("AEMEATH_WAKE_MODEL_DIR")
	if dir == "" {
		dir = DefaultModelDir()
	}
	d, err := New(Config{ModelDir: dir, Phrase: os.Getenv("AEMEATH_WAKE_PHRASE")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, tc := range []struct {
		name, path string
		positive   bool
	}{{"target", positive, true}, {"unrelated", os.Getenv("AEMEATH_WAKE_NEGATIVE_WAV"), false}} {
		if tc.path == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			pcm, err := voiceapi.ReferencePCM(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			// Leading/trailing silence exercises streaming features and trailing blanks.
			stream := make([]int16, SampleRate)
			stream = append(stream, pcm...)
			stream = append(stream, make([]int16, SampleRate*2)...)
			d.Reset()
			hits := 0
			for pos := 0; pos < len(stream); pos += 320 {
				end := pos + 320
				if end > len(stream) {
					end = len(stream)
				}
				hit, err := d.Feed(stream[pos:end])
				if err != nil {
					t.Fatal(err)
				}
				if hit {
					hits++
					m := d.LastMatch()
					t.Logf("match=%s start=%.3fs end=%.3fs", m.Keyword, float64(m.StartSample)/SampleRate, float64(m.EndSample)/SampleRate)
					if m.Keyword != d.phrase || m.StartSample < 0 || m.EndSample <= m.StartSample || m.EndSample > int64(end) {
						t.Fatal("invalid keyword endpoint")
					}
				}
			}
			if tc.positive && hits == 0 {
				t.Fatal("exact wake phrase not detected")
			}
			if !tc.positive && hits > 0 {
				t.Fatalf("unrelated speech triggered%d wake matches", hits)
			}
		})
	}
}
