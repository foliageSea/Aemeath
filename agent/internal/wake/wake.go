// Package wake provides offline Chinese keyword spotting and Silero VAD.
// Models are installed explicitly through InstallWakeModels or the setup CLI.
package wake

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

const SampleRate = 16000
const MaxFeedSamples = SampleRate
const Keyword = "你好小爱"

// The selected WenetSpeech model uses initials and tone-marked finals.
const KeywordTokens = "n ǐ h ǎo x iǎo ài @你好小爱\n"
const BindingVersion = "2.0.1"

var ErrUnavailable = errors.New("offline wake requires a supported native cgo build with sherpa-onnx")
var ErrClosed = errors.New("offline audio detector is closed")

type Config struct {
	ModelDir  string
	Threshold float64
}

// Match uses cumulative 16 kHz samples since Reset. EndSample is exclusive.
// KWS emits token alignment points, not phoneme boundaries: the endpoint is
// last-token alignment plus one 40 ms model frame, clamped to consumed input.
type Match struct {
	Keyword                string
	StartSample, EndSample int64
}

func DefaultAssetDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "Aemeath", "voice")
	}
	return filepath.Join(dir, "Aemeath", "voice")
}
func DefaultModelDir() string { return filepath.Join(DefaultAssetDir(), "models") }
func normalize(c Config) (Config, error) {
	if c.ModelDir == "" {
		c.ModelDir = DefaultModelDir()
	}
	if c.Threshold == 0 {
		c.Threshold = .25
	}
	if math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) || c.Threshold <= 0 || c.Threshold > 1 {
		return c, fmt.Errorf("wake threshold must be in (0,1]")
	}
	return c, nil
}
func checkChunk(pcm []int16) error {
	if len(pcm) > MaxFeedSamples {
		return fmt.Errorf("audio chunk exceeds %d samples", MaxFeedSamples)
	}
	return nil
}
