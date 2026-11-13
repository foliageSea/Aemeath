// Package audioinput captures the system default microphone as 16 kHz mono S16 PCM.
package audioinput

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
)

const (
	sampleRate   = 16000
	frameSamples = 320
	queueFrames  = 50 // One second of audio, plus a reserved terminal-error slot.
)

// Frame contains either 320 owned PCM samples or a terminal error. PCM remains
// valid after the next receive and may be modified by the receiver.
type Frame struct {
	PCM []int16
	Err error
}

// Input owns a microphone. Close is synchronous, safe concurrently and idempotent.
// Frames closes after cleanup. Buffered audio precedes any terminal error;
// explicit Close ends the stream without an error. A final partial frame is
// discarded when capture ends.
type Input interface {
	Frames() <-chan Frame
	Close()
}

var (
	// ErrOverflow indicates capture stopped because its bounded queue filled.
	ErrOverflow = errors.New("microphone capture queue overflow: consumer did not keep up; recording stopped")
	// ErrStopped indicates an unexpected native device stop.
	ErrStopped = errors.New("microphone capture device stopped unexpectedly; check the default input device and microphone permissions")
)

// Open requests microphone permission and starts the system default microphone.
// Cancellation stops capture, releases the device, and delivers ctx.Err(). Native
// capture requires cgo on macOS or Windows.
func Open(ctx context.Context) (Input, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requestPermission(ctx); err != nil {
		return nil, err
	}
	return openWithDevice(ctx, newDevice)
}

// closeDevice must quiesce all callbacks before returning. Data callbacks for a
// device are serialized by the backend; stopped may run concurrently with data.
type captureDevice interface {
	Start() error
	Close()
}
type deviceFactory func(data func([]byte), stopped func()) (captureDevice, error)

type input struct {
	frames         chan Frame
	failed         chan error
	closeRequested chan struct{}
	done           chan struct{}
	closeOnce      sync.Once
	stopping       atomic.Bool
	// Only the serialized data callback accesses the partial frame.
	partial [frameSamples]int16
	used    int
}

func openWithDevice(ctx context.Context, factory deviceFactory) (Input, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	in := &input{
		frames:         make(chan Frame, queueFrames+1),
		failed:         make(chan error, 1),
		closeRequested: make(chan struct{}),
		done:           make(chan struct{}),
	}
	dev, err := factory(in.capture, func() { in.fail(ErrStopped) })
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		in.stopping.Store(true)
		dev.Close()
		return nil, err
	}
	if err := dev.Start(); err != nil {
		in.stopping.Store(true)
		dev.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		in.stopping.Store(true)
		dev.Close()
		return nil, err
	}
	go in.run(ctx, dev)
	return in, nil
}

func (in *input) Frames() <-chan Frame { return in.frames }

func (in *input) Close() {
	in.closeOnce.Do(func() { close(in.closeRequested) })
	<-in.done
}

func (in *input) fail(err error) {
	if in.stopping.CompareAndSwap(false, true) {
		// Only the successful CAS sends, so this never waits for the cleanup worker.
		in.failed <- err
	}
}

func (in *input) capture(pcm []byte) {
	if in.stopping.Load() {
		return
	}
	if len(pcm)%2 != 0 {
		in.fail(errors.New("microphone returned an invalid S16 capture buffer"))
		return
	}
	for len(pcm) > 0 {
		if in.stopping.Load() {
			return
		}
		n := min(frameSamples-in.used, len(pcm)/2)
		for i := 0; i < n; i++ {
			in.partial[in.used+i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		}
		in.used += n
		pcm = pcm[n*2:]
		if in.used != frameSamples {
			continue
		}
		// The callback is the sole PCM producer. Receivers can only decrease len;
		// leaving one slot guarantees cleanup can deliver an error without a reader.
		if len(in.frames) >= queueFrames {
			in.fail(ErrOverflow)
			return
		}
		samples := make([]int16, frameSamples)
		copy(samples, in.partial[:])
		select {
		case in.frames <- Frame{PCM: samples}:
			in.used = 0
		default:
			in.fail(ErrOverflow)
			return
		}
	}
}

func (in *input) run(ctx context.Context, dev captureDevice) {
	var err error
	select {
	case err = <-in.failed:
	case <-ctx.Done():
		err = ctx.Err()
	case <-in.closeRequested:
	}
	in.stopping.Store(true)
	// Never stop or uninitialize a device on its own callback thread.
	dev.Close()
	// Preserve a device/overflow error that raced with cancellation or Close.
	select {
	case captureErr := <-in.failed:
		err = captureErr
	default:
	}
	if err != nil {
		in.frames <- Frame{Err: err}
	}
	close(in.frames)
	close(in.done)
}
