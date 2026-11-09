//go:build cgo && ((darwin && (amd64 || arm64)) || (linux && (amd64 || arm64 || arm)) || (windows && (amd64 || arm64 || 386)))

package wake

import (
	"fmt"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/v2/sherpa_onnx"
	"math"
	"path/filepath"
	"sync"
)

type Detector struct {
	mu          sync.Mutex
	spotter     *sherpa.KeywordSpotter
	stream      *sherpa.OnlineStream
	total, base int64
	last        Match
	recent      []float32
}

func New(c Config) (*Detector, error) {
	c, err := normalize(c)
	if err != nil {
		return nil, err
	}
	if err = verifyModels(c.ModelDir); err != nil {
		return nil, fmt.Errorf("wake models unavailable: %w; run aemeath-voice-assets", err)
	}
	cfg := sherpa.KeywordSpotterConfig{
		FeatConfig:     sherpa.FeatureConfig{SampleRate: SampleRate, FeatureDim: 80},
		ModelConfig:    sherpa.OnlineModelConfig{Transducer: sherpa.OnlineTransducerModelConfig{Encoder: filepath.Join(c.ModelDir, "encoder.onnx"), Decoder: filepath.Join(c.ModelDir, "decoder.onnx"), Joiner: filepath.Join(c.ModelDir, "joiner.onnx")}, Tokens: filepath.Join(c.ModelDir, "tokens.txt"), NumThreads: 1, Provider: "cpu", ModelType: "zipformer2"},
		MaxActivePaths: 4, KeywordsScore: 1, KeywordsThreshold: float32(c.Threshold), KeywordsBuf: KeywordTokens, KeywordsBufSize: len(KeywordTokens),
	}
	s := sherpa.NewKeywordSpotter(&cfg)
	if s == nil {
		return nil, fmt.Errorf("sherpa-onnx could not load Chinese wake model")
	}
	d := &Detector{spotter: s}
	d.stream = sherpa.NewKeywordStream(s)
	if !validNativeStream(d.stream) {
		d.Close()
		return nil, fmt.Errorf("sherpa-onnx could not create keyword stream")
	}
	return d, nil
}
func (d *Detector) Feed(pcm []int16) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.spotter == nil {
		return false, ErrClosed
	}
	if err := checkChunk(pcm); err != nil {
		return false, err
	}
	if len(pcm) == 0 {
		return false, nil
	}
	// Recreate streams every 30 seconds to bound native feature/history storage.
	// Replay a bounded three-second overlap so phrases across maintenance
	// boundaries are retained without growing native feature history.
	if d.total-d.base >= 30*SampleRate {
		d.replaceStream()
		if d.stream == nil {
			return false, ErrClosed
		}
		d.base -= int64(len(d.recent))
		if len(d.recent) > 0 {
			d.stream.AcceptWaveform(SampleRate, d.recent)
		}
	}
	samples := make([]float32, len(pcm))
	for i, v := range pcm {
		samples[i] = float32(v) / 32768
	}
	d.stream.AcceptWaveform(SampleRate, samples)
	d.total += int64(len(pcm))
	if d.recent == nil {
		d.recent = make([]float32, 0, 3*SampleRate)
	}
	if overflow := len(d.recent) + len(samples) - 3*SampleRate; overflow > 0 {
		copy(d.recent, d.recent[overflow:])
		d.recent = d.recent[:len(d.recent)-overflow]
	}
	d.recent = append(d.recent, samples...)
	hit := false
	for n := 0; d.spotter.IsReady(d.stream); n++ {
		if n >= 128 {
			d.replaceStream()
			return false, fmt.Errorf("keyword decode budget exceeded")
		}
		d.spotter.Decode(d.stream)
		r, err := keywordResult(d.spotter, d.stream)
		if err != nil {
			return false, err
		}
		if r.Keyword != "" {
			if r.Keyword == Keyword && len(r.Timestamps) > 0 {
				start := d.base + int64(math.Round((r.StartTime+r.Timestamps[0])*SampleRate))
				end := d.base + int64(math.Round((r.StartTime+r.Timestamps[len(r.Timestamps)-1]+.04)*SampleRate))
				if end > d.total {
					end = d.total
				}
				if start < d.base {
					start = d.base
				}
				if end < start {
					end = start
				}
				if end > d.last.EndSample {
					d.last = Match{Keyword: Keyword, StartSample: start, EndSample: end}
					hit = true
				}
			}
			d.spotter.Reset(d.stream)
		}
	}
	return hit, nil
}
func (d *Detector) replaceStream() {
	if d.stream != nil {
		sherpa.DeleteOnlineStream(d.stream)
	}
	d.stream = sherpa.NewKeywordStream(d.spotter)
	d.base = d.total
	if !validNativeStream(d.stream) {
		d.stream = nil
	}
}
func (d *Detector) LastMatch() Match         { d.mu.Lock(); defer d.mu.Unlock(); return d.last }
func (d *Detector) KeywordEndSamples() int64 { return d.LastMatch().EndSample }
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.spotter == nil {
		return
	}
	d.total = 0
	d.recent = d.recent[:0]
	d.replaceStream()
	d.last = Match{}
}
func (d *Detector) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stream != nil {
		sherpa.DeleteOnlineStream(d.stream)
		d.stream = nil
	}
	if d.spotter != nil {
		sherpa.DeleteKeywordSpotter(d.spotter)
		d.spotter = nil
	}
}

type VAD struct {
	mu      sync.Mutex
	native  *sherpa.VoiceActivityDetector
	pending []float32
	speech  bool
	total   int64
}

func NewVAD(dir string) (*VAD, error) {
	if dir == "" {
		dir = DefaultModelDir()
	}
	if err := verifyFile(filepath.Join(dir, "silero_vad.onnx"), vadAsset); err != nil {
		return nil, fmt.Errorf("Silero VAD unavailable: %w; run aemeath-voice-assets", err)
	}
	c := sherpa.VadModelConfig{SileroVad: sherpa.SileroVadModelConfig{Model: filepath.Join(dir, "silero_vad.onnx"), Threshold: .5, MinSilenceDuration: .10, MinSpeechDuration: .10, WindowSize: 512, MaxSpeechDuration: 30}, SampleRate: SampleRate, NumThreads: 1, Provider: "cpu"}
	v := sherpa.NewVoiceActivityDetector(&c, 20)
	if v == nil {
		return nil, fmt.Errorf("sherpa-onnx could not load Silero VAD")
	}
	return &VAD{native: v, pending: make([]float32, 0, 512)}, nil
}
func (v *VAD) Speech(pcm []int16) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.native == nil {
		return false, ErrClosed
	}
	if err := checkChunk(pcm); err != nil {
		return false, err
	}
	for _, s := range pcm {
		v.pending = append(v.pending, float32(s)/32768)
		if len(v.pending) == 512 {
			v.native.AcceptWaveform(v.pending)
			v.pending = v.pending[:0]
			v.speech = v.native.IsSpeech()
			for !v.native.IsEmpty() {
				v.native.Pop()
			}
			v.total += 512
			// VAD itself uses a bounded ring. Completed segments are immediately popped.
		}
	}
	return v.speech, nil
}
func (v *VAD) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.native != nil {
		v.native.Reset()
	}
	v.pending = v.pending[:0]
	v.speech = false
	v.total = 0
}
func (v *VAD) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.native != nil {
		sherpa.DeleteVoiceActivityDetector(v.native)
		v.native = nil
	}
	v.pending = nil
}
