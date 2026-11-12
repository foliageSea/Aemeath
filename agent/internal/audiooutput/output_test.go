package audiooutput

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

func testPlayer(volume int) *Player {
	return &Player{rate: 16000, volume: volume, queue: make([]int16, 32000)}
}

func TestBoundedQueueWraparoundVolumeAndDrain(t *testing.T) {
	p := testPlayer(50)
	pcm := make([]int16, 40000)
	for i := range pcm {
		pcm[i] = int16(i)
	}
	if n, err := p.Write(pcm); err != nil || n != 32000 {
		t.Fatalf("write=%d %v", n, err)
	}
	if n, err := p.Write(pcm); err != nil || n != 0 {
		t.Fatalf("full write=%d %v", n, err)
	}
	out := make([]byte, 320*2)
	p.render(out)
	for i := 0; i < 320; i++ {
		if got := int16(binary.LittleEndian.Uint16(out[i*2:])); got != pcm[i]/2 {
			t.Fatalf("sample %d=%d", i, got)
		}
	}
	if n, err := p.Write(pcm[:500]); err != nil || n != 320 {
		t.Fatalf("wrap write=%d %v", n, err)
	}
	p.Finish()
	if _, err := p.Write(pcm); err == nil {
		t.Fatal("write after finish accepted")
	}
	for i := 0; i < 100; i++ {
		p.render(out)
	}
	if started, drained, err := p.Status(); !started || drained || err != nil {
		t.Fatalf("premature drain: %v %v %v", started, drained, err)
	}
	// Completion requires callback silence and wall time, not just enqueue EOF.
	for i := 0; i < 8; i++ {
		p.render(out)
	}
	p.mu.Lock()
	p.lastAudio = time.Now().Add(-time.Second)
	p.mu.Unlock()
	if started, drained, err := p.Status(); !started || !drained || err != nil {
		t.Fatalf("not drained: %v %v %v", started, drained, err)
	}
	p.Close()
	if n, err := p.Write(pcm); n != 0 || err == nil {
		t.Fatal("closed write accepted")
	}
}

func TestMuteUnderrunAndUnexpectedStop(t *testing.T) {
	p := testPlayer(0)
	out := make([]byte, 640)
	for i := range out {
		out[i] = 255
	}
	p.render(out)
	if started, drained, _ := p.Status(); started || drained {
		t.Fatal("underrun marked playback complete")
	}
	_, _ = p.Write([]int16{-32768, 32767})
	p.render(out)
	for _, b := range out {
		if b != 0 {
			t.Fatal("mute or underrun emitted nonzero sample")
		}
	}
	p.stopped()
	if _, _, err := p.Status(); err == nil {
		t.Fatal("output stop not reported")
	}
	p.Close()
	p.Close()
}

func TestCallbackAndCancellationAreRaceSafe(t *testing.T) {
	p := testPlayer(100)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_, _ = p.Write([]int16{1, 2, 3})
			p.Status()
		}
		p.Finish()
	}()
	go func() {
		defer wg.Done()
		out := make([]byte, 640)
		for i := 0; i < 1000; i++ {
			p.render(out)
		}
		p.Close()
	}()
	wg.Wait()
	p.Close()
}

func TestNewRejectsInvalidFormatBeforeOpeningHardware(t *testing.T) {
	for _, tc := range [][2]int{{0, 50}, {16000, -1}, {16000, 101}} {
		if _, err := New(tc[0], tc[1]); err == nil {
			t.Fatal("invalid format accepted")
		}
	}
}
