// Package audiooutput plays signed 16-bit mono PCM on the host's default output.
// Its queue is bounded and writes never wait for the hardware, so voice commands
// remain responsive while streaming or playing long clips.
package audiooutput

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

// Player owns one output device and one clip or stream. The caller serializes
// Write, Finish, Status and Close; the native callback runs concurrently.
type Player struct {
	mu                        sync.Mutex
	queue                     []int16
	head, size                int
	rate, volume              int
	finished, started, closed bool
	tailSamples               int
	lastAudio                 time.Time
	err                       error
	closeDevice               func()
}

// New opens the OS default playback device (not the agent microphone/device).
// A native output failure is an error; audio is never rerouted elsewhere.
func New(sampleRate, volume int) (*Player, error) {
	if sampleRate < 8000 || sampleRate > 192000 || volume < 0 || volume > 100 {
		return nil, errors.New("invalid console audio format or volume")
	}
	p := &Player{rate: sampleRate, volume: volume, queue: make([]int16, sampleRate*2)}
	closeDevice, err := startDevice(p, sampleRate)
	if err != nil {
		return nil, err
	}
	p.closeDevice = closeDevice
	return p, nil
}

// Write copies as much PCM as fits. A zero/short write is backpressure, not EOF.
func (p *Player) Write(pcm []int16) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, errors.New("console audio output is closed")
	}
	if p.err != nil {
		return 0, p.err
	}
	if p.finished {
		return 0, errors.New("console audio input has ended")
	}
	n := min(len(pcm), len(p.queue)-p.size)
	for i, v := range pcm[:n] {
		p.queue[(p.head+p.size+i)%len(p.queue)] = v
	}
	p.size += n
	return n, nil
}

// Finish seals the input. Completion is delayed until the native callback has
// consumed it and a conservative silent tail has passed through the output.
func (p *Player) Finish() { p.mu.Lock(); p.finished = true; p.mu.Unlock() }

func (p *Player) Status() (started, drained bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started, p.drained(time.Now()), p.err
}

// Native APIs do not expose a portable DAC cursor. The requested device buffer
// is 3 x 20ms; allow 150ms of callback silence AND wall time after the last PCM
// before declaring drain, instead of declaring completion when Write returns.
// Backend/OS latency above that requested buffer can still affect this estimate.
func (p *Player) drained(now time.Time) bool {
	return p.finished && p.started && p.size == 0 && p.tailSamples >= p.rate*150/1000 && now.Sub(p.lastAudio) >= 150*time.Millisecond
}

// render is called by the native device. An underrun emits silence, not EOF.
func (p *Player) render(output []byte) {
	clear(output)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.err != nil {
		return
	}
	n := min(len(output)/2, p.size)
	for i := 0; i < n; i++ {
		v := int16(int32(p.queue[(p.head+i)%len(p.queue)]) * int32(p.volume) / 100)
		binary.LittleEndian.PutUint16(output[i*2:], uint16(v))
	}
	p.head = (p.head + n) % len(p.queue)
	p.size -= n
	if n > 0 {
		p.started = true
		p.lastAudio = time.Now()
		p.tailSamples = 0
	} else if p.started {
		p.tailSamples = min(p.tailSamples+len(output)/2, p.rate)
	}
}

func (p *Player) stopped() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed && !p.drained(time.Now()) {
		p.err = errors.New("console audio output stopped unexpectedly")
	}
}

// Close stops playback and discards queued PCM. It is idempotent and releases
// the device outside the callback mutex, avoiding native shutdown deadlocks.
func (p *Player) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.size = 0
	closeDevice := p.closeDevice
	p.mu.Unlock()
	if closeDevice != nil {
		closeDevice()
	}
}
