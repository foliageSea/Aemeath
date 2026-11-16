// Command wake-cue-gen synthesizes the wake cue with the Fish voice configured
// in the desktop settings. It never prints credentials.
//
//	go run ./scripts/wake-cue-gen -out /tmp/aemeath-wake-cue/fish -takes 3 -text "我在。"
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aemeath/agent/internal/fishapi"
	"aemeath/agent/internal/voiceapi"
)

type settings struct {
	Voice struct {
		FishAPIKey        string `json:"fishAPIKey"`
		FishBaseURL       string `json:"fishBaseURL"`
		FishModel         string `json:"fishModel"`
		FishReferenceID   string `json:"fishReferenceID"`
		FishReferenceText string `json:"fishReferenceText"`
		FishLatency       string `json:"fishLatency"`
		ReferenceAudio    string `json:"referenceAudio"`
	} `json:"voice"`
}

func main() {
	home, _ := os.UserHomeDir()
	path := flag.String("settings", filepath.Join(home, "Library", "Application Support", "Aemeath", "settings.json"), "desktop settings file")
	out := flag.String("out", "wake-cue", "output file prefix")
	text := flag.String("text", "我在。", "text to synthesize")
	takes := flag.Int("takes", 1, "number of takes")
	flag.Parse()
	if err := run(*path, *out, *text, *takes); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, out, text string, takes int) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s settings
	if err = json.Unmarshal(raw, &s); err != nil {
		return errors.New("settings file is not valid JSON")
	}
	v := s.Voice
	if strings.TrimSpace(v.FishAPIKey) == "" {
		return errors.New("Fish API key is not configured")
	}
	client, err := fishapi.New(fishapi.Config{BaseURL: v.FishBaseURL, APIKey: v.FishAPIKey, Model: v.FishModel, ReferenceID: v.FishReferenceID, ReferenceAudio: v.ReferenceAudio, ReferenceText: v.FishReferenceText, Latency: v.FishLatency})
	if err != nil {
		return redact(err, v.FishAPIKey)
	}
	for i := 1; i <= takes; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		parts := make(chan string, 1)
		parts <- text
		close(parts)
		var pcm []int16
		err = client.Stream(ctx, parts, func(chunk []int16) error { pcm = append(pcm, chunk...); return nil })
		cancel()
		if err != nil {
			return redact(err, v.FishAPIKey)
		}
		name := fmt.Sprintf("%s-%d.wav", out, i)
		if err = os.WriteFile(name, voiceapi.EncodeWAV(pcm, 16000), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s %.3fs\n", name, float64(len(pcm))/16000)
	}
	return nil
}

func redact(err error, key string) error {
	return errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
}
