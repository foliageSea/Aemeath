package voice

import (
	"aemeath/agent/internal/fishapi"
	"aemeath/agent/internal/voiceapi"
	"context"
	"errors"
)

type fishSpeech struct {
	asr    *voiceapi.Client
	config fishapi.Config
}

func (s fishSpeech) Recognize(ctx context.Context, pcm []int16) (string, error) {
	return s.asr.Recognize(ctx, pcm)
}
func (s fishSpeech) Stream(ctx context.Context, text <-chan string, onAudio func([]int16) error) error {
	client, err := fishapi.New(s.config)
	if err != nil {
		return err
	}
	return client.Stream(ctx, text, onAudio)
}
func (s fishSpeech) Synthesize(ctx context.Context, text, _ string) ([]int16, error) {
	parts := make(chan string, 1)
	parts <- text
	close(parts)
	var pcm []int16
	err := s.Stream(ctx, parts, func(chunk []int16) error {
		if len(chunk) > maxPlaybackSamples-len(pcm) {
			return errors.New("Fish audio exceeds 120 seconds")
		}
		pcm = append(pcm, chunk...)
		return nil
	})
	return pcm, err
}
func configuredSpeech(c Config) speechAPI {
	asr := voiceapi.New(c.MiMoBaseURL, c.MiMoAPIKey)
	if c.TTSProvider == "fish" {
		return fishSpeech{asr: asr, config: fishapi.Config{BaseURL: c.FishBaseURL, APIKey: c.FishAPIKey, Model: c.FishModel, ReferenceID: c.FishReferenceID, ReferenceAudio: c.ReferenceAudio, ReferenceText: c.FishReferenceText, Latency: c.FishLatency}}
	}
	return managedSpeech{asr}
}
