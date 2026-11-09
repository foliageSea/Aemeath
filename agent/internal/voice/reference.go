package voice

import (
	"aemeath/agent/internal/voiceapi"
	"aemeath/agent/internal/wake"
	"context"
)

type managedSpeech struct{ *voiceapi.Client }

func (s managedSpeech) Synthesize(ctx context.Context, text, path string) ([]int16, error) {
	if err := wake.UpgradeReference(ctx, path); err != nil {
		return nil, err
	}
	return s.Client.Synthesize(ctx, text, path)
}

func managedReference(path string) ([]int16, error) {
	if err := wake.UpgradeReference(context.Background(), path); err != nil {
		return nil, err
	}
	return voiceapi.ReferencePCM(path)
}
