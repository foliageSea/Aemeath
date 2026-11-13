package voice

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxReplayTextBytes bounds explicit TTS regeneration of a saved reply.
	MaxReplayTextBytes = maxSpeechTextBytes
	maxReplayTurns     = 10
	maxReplaySamples   = sampleRate * 300 // Five minutes of 16-bit mono PCM, about 9.6 MB.
)

type cachedReply struct {
	key  string
	turn uint32
	text string
	pcm  []int16
}

// Copy audio independently of the playback queue: Fish compacts its stream
// buffer, and worker-owned segment buffers must not become mutable cache data.
func (s *session) captureReplay(pcm []int16) {
	if !s.replayCapture {
		return
	}
	if len(pcm) == 0 || len(pcm) > maxPlaybackSamples-len(s.replayPCM) {
		s.replayCapture = false
		s.replayPCM = nil
		return
	}
	s.replayPCM = append(s.replayPCM, pcm...)
}

// Only complete replies are published. Canceled/failed synthesis drops its
// pending PCM in abort, without invalidating previously completed replies.
func (s *session) commitReplay() {
	if !s.replayCapture || len(s.replayPCM) == 0 || s.state.Response == "" || s.state.Turn == 0 {
		return
	}
	key := s.state.ReplayingKey
	if key == "" {
		key = ReplayTextKey(s.state.Response)
	}
	s.replayCache = append(s.replayCache, cachedReply{key: key, turn: s.state.Turn, text: s.state.Response, pcm: s.replayPCM})
	s.replayPCM = nil
	s.replayCapture = false
	total := 0
	for _, reply := range s.replayCache {
		total += len(reply.pcm)
	}
	for len(s.replayCache) > maxReplayTurns || total > maxReplaySamples {
		total -= len(s.replayCache[0].pcm)
		s.replayCache[0] = cachedReply{}
		s.replayCache = s.replayCache[1:]
	}
	s.state.ReplayTurns = make([]uint32, len(s.replayCache))
	s.state.ReplayKeys = make([]string, len(s.replayCache))
	for i, reply := range s.replayCache {
		s.state.ReplayTurns[i] = reply.turn
		s.state.ReplayKeys[i] = reply.key
	}
	s.emitState()
}

// ReplayTextKey identifies saved reply text without sending full text in snapshots.
func ReplayTextKey(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(text))))
}

// savedReplySpeech only feeds existing text through the normal sentence/filter
// and TTS pipeline. It has no model, tools, history, or token accounting.
type savedReplySpeech string

func (text savedReplySpeech) Reply(context.Context, string) (string, error) { return string(text), nil }
func (savedReplySpeech) Clear()                                             {}

func (s *session) respeak(text string) error {
	if len(text) > MaxReplayTextBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		s.state.Notice = "回复文本为空、无效或过长，无法重新合成语音。"
		s.emitState()
		return nil
	}
	text = strings.TrimSpace(text)
	key := ReplayTextKey(text)
	// Recheck in the event loop: a previous regeneration may have completed
	// between rendering the button and receiving this explicit command.
	for i := len(s.replayCache) - 1; i >= 0; i-- {
		if s.replayCache[i].key == key {
			return s.replay(s.replayCache[i].turn)
		}
	}
	if err := s.begin(); err != nil {
		return err
	}
	s.state.Response = text
	s.state.ReplayingKey = key
	s.submit("respeak", text, nil)
	return s.phase("synthesizing", "无可用语音缓存，正在按当前音色重新合成（可能产生 TTS 费用）。")
}

func (s *session) replay(turn uint32) error {
	var reply cachedReply
	for _, cached := range s.replayCache {
		if cached.turn == turn {
			reply = cached
			break
		}
	}
	if len(reply.pcm) == 0 {
		// A queued command may race with clear/eviction. Do not cancel a newer
		// operation or terminate the connection for a stale replay request.
		s.state.Notice = "此回复的语音缓存已失效，无法重播。"
		s.emitState()
		return nil
	}
	// A fresh logical and wire turn isolates late cloud results and firmware
	// acknowledgements. No worker job means no ASR/LLM/TTS or memory changes.
	if err := s.begin(); err != nil {
		return err
	}
	s.state.ReplayingTurn = reply.turn
	s.state.ReplayingKey = reply.key
	s.state.Response = reply.text
	s.state.GeneratedSegments = 1
	s.cloudStarted = time.Now()
	s.generationDone = true
	s.queuedAudio = []speechClip{{pcm: reply.pcm, text: reply.text}}
	if err := s.phase("speaking", "正在重播已缓存的回复语音"); err != nil {
		return err
	}
	return s.nextSegment(false)
}
