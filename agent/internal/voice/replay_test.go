package voice

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func replayTestSession(t *testing.T) (*session, *[]*fakeConsoleOutput) {
	t.Helper()
	outputs := &[]*fakeConsoleOutput{}
	s := &session{ctx: context.Background(), standalone: true, cfg: Config{PlaybackOutput: "console"}, jobs: make(chan work, 1), emit: func(State) {}}
	s.newOutput = func(Config) (consoleOutput, error) {
		out := &fakeConsoleOutput{}
		*outputs = append(*outputs, out)
		return out, nil
	}
	t.Cleanup(func() { _ = s.abort(false) })
	return s, outputs
}

func replayResult(t *testing.T, s *session, r result) {
	t.Helper()
	r.turn, r.generation = s.state.Turn, s.generation
	if err := s.result(r); err != nil {
		t.Fatal(err)
	}
}

func startReplayConversation(t *testing.T, s *session) uint32 {
	t.Helper()
	if err := s.command(Command{Kind: "text", Text: "问题"}); err != nil {
		t.Fatal(err)
	}
	<-s.jobs
	replayResult(t, s, result{phase: "response", text: "第一句。第二句。", final: true})
	return s.state.Turn
}

func TestReplaySegmentedReplyUsesOriginalPCMWithoutCloudWork(t *testing.T) {
	s, outputs := replayTestSession(t)
	source := startReplayConversation(t, s)
	first := []int16{1, 2}
	replayResult(t, s, result{phase: "audio_segment", pcm: first, text: "第一句。"})
	first[0] = 99 // Worker buffers must not alias the replay cache.
	if len(s.state.ReplayTurns) != 0 {
		t.Fatal("partial reply became replayable")
	}
	replayResult(t, s, result{phase: "audio_segment", pcm: []int16{3, 4}, text: "第二句。", final: true})
	if !reflect.DeepEqual(s.state.ReplayTurns, []uint32{source}) {
		t.Fatal(s.state.ReplayTurns)
	}
	ctx := s.state.Context
	originalWire := s.wireTurn
	if err := s.command(Command{Kind: "replay", Turn: source}); err != nil {
		t.Fatal(err)
	}
	if s.state.Transcript != "" || s.state.ReplayingTurn != source || s.state.Turn == source || s.wireTurn <= originalWire {
		t.Fatalf("replay not isolated: %+v", s.state)
	}
	if len(*outputs) != 2 {
		t.Fatalf("outputs = %d", len(*outputs))
	}
	pcm, finished, _ := (*outputs)[1].snapshot()
	if !reflect.DeepEqual(pcm, []int16{1, 2, 3, 4}) || !finished {
		t.Fatalf("replayed %v, finished=%v", pcm, finished)
	}
	if _, _, closed := (*outputs)[0].snapshot(); !closed {
		t.Fatal("old playback not stopped")
	}
	if len(s.jobs) != 0 || !reflect.DeepEqual(s.state.Context, ctx) {
		t.Fatal("replay scheduled cloud work or altered context")
	}
	// Late worker events cannot overwrite replay, including final responses.
	if err := s.result(result{turn: source, generation: s.generation, phase: "response", text: "stale"}); err != nil {
		t.Fatal(err)
	}
	if s.state.Response != "第一句。第二句。" {
		t.Fatal(s.state.Response)
	}
	(*outputs)[1].allowDrain.Store(true)
	if err := s.pumpConsole(); err != nil {
		t.Fatal(err)
	}
	if s.active || s.state.ReplayingTurn != 0 || s.state.Phase != "idle" || len(s.state.ReplayTurns) != 1 {
		t.Fatalf("did not finish: %+v", s.state)
	}
	// Replaying repeatedly must not create duplicate cache entries.
	if err := s.command(Command{Kind: "replay", Turn: source}); err != nil {
		t.Fatal(err)
	}
	if len(s.replayCache) != 1 || len(s.jobs) != 0 {
		t.Fatal("replay recached or called cloud")
	}
	if err := s.command(Command{Kind: "text", Text: "新问题"}); err != nil {
		t.Fatal(err)
	}
	if _, _, closed := (*outputs)[2].snapshot(); !closed || s.state.ReplayingTurn != 0 || s.state.Transcript != "新问题" {
		t.Fatal("new question did not interrupt replay")
	}
}

func TestReplayStreamingRetainsCompactedAudioAndSupportsCancel(t *testing.T) {
	s, outputs := replayTestSession(t)
	source := startReplayConversation(t, s)
	replayResult(t, s, result{phase: "audio_stream_start"})
	replayResult(t, s, result{phase: "audio_stream_chunk", pcm: []int16{5, 6}})
	replayResult(t, s, result{phase: "audio_stream_chunk", pcm: []int16{7, 8}})
	if !reflect.DeepEqual(s.play.pcm, []int16{7, 8}) {
		t.Fatal("test did not compact streaming buffer")
	}
	replayResult(t, s, result{phase: "audio_stream_end", samples: 4})
	if !reflect.DeepEqual(s.state.ReplayTurns, []uint32{source}) {
		t.Fatal("complete stream not cached")
	}
	if err := s.command(Command{Kind: "replay", Turn: source}); err != nil {
		t.Fatal(err)
	}
	pcm, _, _ := (*outputs)[1].snapshot()
	if !reflect.DeepEqual(pcm, []int16{5, 6, 7, 8}) {
		t.Fatal(pcm)
	}
	if err := s.command(Command{Kind: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, _, closed := (*outputs)[1].snapshot(); !closed || s.state.ReplayingTurn != 0 || len(s.state.ReplayTurns) != 1 {
		t.Fatal("cancel lost cached reply or left audio active")
	}
}

func TestReplayIncompleteInvalidAndClearedAudioIsUnavailable(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, _ := replayTestSession(t)
		startReplayConversation(t, s)
		replayResult(t, s, result{phase: "audio_segment", pcm: []int16{1}})
		if fail {
			replayResult(t, s, result{phase: "synthesizing", err: errors.New("failed")})
		} else if err := s.command(Command{Kind: "cancel"}); err != nil {
			t.Fatal(err)
		}
		if len(s.replayCache) != 0 || len(s.replayPCM) != 0 {
			t.Fatal("partial audio retained")
		}
	}
	s, _ := replayTestSession(t)
	source := startReplayConversation(t, s)
	replayResult(t, s, result{phase: "audio_segment", pcm: []int16{1}, final: true})
	clone := s.state.Clone()
	clone.ReplayTurns[0] = 99
	if s.state.ReplayTurns[0] != source {
		t.Fatal("snapshot aliases replay IDs")
	}
	if err := s.command(Command{Kind: "clear"}); err != nil {
		t.Fatal(err)
	}
	<-s.jobs
	if len(s.replayCache) != 0 || len(s.state.ReplayTurns) != 0 {
		t.Fatal("clear retained audio")
	}
	if err := s.command(Command{Kind: "text", Text: "新问题"}); err != nil {
		t.Fatal(err)
	}
	previous := s.state.Turn
	if err := s.command(Command{Kind: "replay", Turn: source}); err != nil {
		t.Fatal(err)
	}
	if !s.active || s.state.Turn != previous || s.state.Transcript != "新问题" || s.state.Notice == "" {
		t.Fatal("stale replay disturbed new operation")
	}
	fresh, _ := replayTestSession(t)
	if len(fresh.replayCache) != 0 || len(fresh.state.ReplayTurns) != 0 {
		t.Fatal("new connection inherited cache")
	}
}

func TestReplayCacheEvictsOldestByCountAndDuration(t *testing.T) {
	s, _ := replayTestSession(t)
	for i := 1; i <= maxReplayTurns+2; i++ {
		s.state.Turn, s.state.Response = uint32(i), "回复"
		s.replayCapture = true
		s.captureReplay([]int16{int16(i)})
		s.commitReplay()
	}
	if len(s.replayCache) != maxReplayTurns || s.state.ReplayTurns[0] != 3 {
		t.Fatal(s.state.ReplayTurns)
	}
	s.replayCache = nil
	for i := 1; i <= 3; i++ {
		s.state.Turn = uint32(i)
		s.replayCapture = true
		s.captureReplay(make([]int16, maxPlaybackSamples))
		s.commitReplay()
	}
	if !reflect.DeepEqual(s.state.ReplayTurns, []uint32{2, 3}) {
		t.Fatal(s.state.ReplayTurns)
	}
	s.replayCapture = true
	s.captureReplay(make([]int16, maxPlaybackSamples+1))
	if s.replayCapture || s.replayPCM != nil {
		t.Fatal("oversized cache accepted")
	}
}

func TestReplayGenerationDoneCommitsAfterLastSegmentDrained(t *testing.T) {
	s, outputs := replayTestSession(t)
	source := startReplayConversation(t, s)
	replayResult(t, s, result{phase: "audio_segment", pcm: []int16{1, 2}, text: "完整回复", final: false})
	(*outputs)[0].allowDrain.Store(true)
	if err := s.pumpConsole(); err != nil {
		t.Fatal(err)
	}
	if s.play != nil || !s.active || len(s.state.ReplayTurns) != 0 {
		t.Fatal("unexpected state before generation_done")
	}
	replayResult(t, s, result{phase: "generation_done"})
	if s.active || !reflect.DeepEqual(s.state.ReplayTurns, []uint32{source}) {
		t.Fatalf("not committed: %+v", s.state)
	}
	if err := s.command(Command{Kind: "replay", Turn: source}); err != nil {
		t.Fatal(err)
	}
	pcm, _, _ := (*outputs)[1].snapshot()
	if !reflect.DeepEqual(pcm, []int16{1, 2}) {
		t.Fatal(pcm)
	}
}

func TestReplayStreamSnapshotOwnershipAndFailurePreservesPreviousCache(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		s, _ := replayTestSession(t)
		source := startReplayConversation(t, s)
		replayResult(t, s, result{phase: "audio_segment", pcm: []int16{1}, final: true})
		startReplayConversation(t, s)
		s.emit = func(snapshot State) {
			if len(snapshot.ReplayTurns) > 0 {
				snapshot.ReplayTurns[0] = 999
			}
		}
		replayResult(t, s, result{phase: "audio_stream_start"})
		if s.state.ReplayTurns[0] != source {
			t.Fatal("stream snapshot aliases replay IDs")
		}
		replayResult(t, s, result{phase: "audio_stream_chunk", pcm: []int16{2, 3}})
		if mismatch {
			replayResult(t, s, result{phase: "audio_stream_end", samples: 3})
		} else {
			replayResult(t, s, result{phase: "synthesizing", err: errors.New("stream failed")})
		}
		if s.active || len(s.replayPCM) != 0 || !reflect.DeepEqual(s.state.ReplayTurns, []uint32{source}) || len(s.replayCache) != 1 {
			t.Fatalf("failed stream damaged cache: %+v", s.state)
		}
	}
}

func TestReplayDeviceCreditsAndStaleAcknowledgements(t *testing.T) {
	s := recordTestSession(t)
	s.state.Response = "回复"
	s.replayCapture = true
	s.captureReplay([]int16{10, 11})
	s.commitReplay()
	source := s.state.Turn
	if err := s.command(Command{Kind: "replay", Turn: source}); err != nil {
		t.Fatal(err)
	}
	wire := s.wireTurn
	recordControl(t, s, message{Type: "credit", Turn: wire, Frames: 1})
	if s.play.offset != 2 || !s.play.ended {
		t.Fatal("replay did not use device credit flow")
	}
	recordControl(t, s, message{Type: "played", Turn: wire - 1})
	if s.play == nil {
		t.Fatal("stale played drained replay")
	}
	recordControl(t, s, message{Type: "playing", Turn: wire})
	recordControl(t, s, message{Type: "played", Turn: wire})
	if s.play != nil || s.active || s.state.ReplayingTurn != 0 || len(s.jobs) != 0 {
		t.Fatalf("bad completion: %+v", s.state)
	}
}
