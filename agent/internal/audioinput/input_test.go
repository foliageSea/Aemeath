package audioinput

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDevice struct {
	mu      sync.Mutex
	data    func([]byte)
	stopped func()
	start   func() error
	closed  bool
	closes  atomic.Int32
}

func (d *fakeDevice) Start() error {
	if d.start != nil {
		return d.start()
	}
	return nil
}
func (d *fakeDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	d.closes.Add(1)
	// Real backends may notify Stop while uninitializing. This is not a failure.
	d.stopped()
}
func (d *fakeDevice) emit(pcm []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.data(pcm)
	}
}
func (d *fakeDevice) factory(data func([]byte), stopped func()) (captureDevice, error) {
	d.data, d.stopped = data, stopped
	return d, nil
}
func openFake(t *testing.T, ctx context.Context, d *fakeDevice) Input {
	t.Helper()
	in, err := openWithDevice(ctx, d.factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Close)
	return in
}
func pcmBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	return data
}
func readFrame(t *testing.T, in Input) (Frame, bool) {
	t.Helper()
	select {
	case frame, ok := <-in.Frames():
		return frame, ok
	case <-time.After(2 * time.Second):
		t.Fatal("capture stream did not make progress")
		return Frame{}, false
	}
}
func waitCleanup(t *testing.T, in Input) {
	t.Helper()
	select {
	case <-in.(*input).done:
	case <-time.After(2 * time.Second):
		t.Fatal("capture did not clean up without a consumer")
	}
}

func TestCaptureFramesAcrossCallbackBoundaries(t *testing.T) {
	d := &fakeDevice{}
	in := openFake(t, context.Background(), d)
	samples := make([]int16, frameSamples*3+17)
	for i := range samples {
		samples[i] = int16(i*101 - 32000)
	}
	// Smaller and larger callbacks must preserve every signed sample and ordering.
	start := 0
	for _, n := range []int{1, 127, 700, len(samples) - 828} {
		bytes := pcmBytes(samples[start : start+n])
		d.emit(bytes)
		clear(bytes) // Native callback memory is borrowed and immediately reusable.
		start += n
	}
	var previous []int16
	for frameIndex := 0; frameIndex < 3; frameIndex++ {
		frame, ok := readFrame(t, in)
		if !ok || frame.Err != nil || len(frame.PCM) != frameSamples {
			t.Fatalf("invalid frame: %+v, open=%v", frame, ok)
		}
		for i, sample := range frame.PCM {
			if want := samples[frameIndex*frameSamples+i]; sample != want {
				t.Fatalf("sample %d = %d, want %d", frameIndex*frameSamples+i, sample, want)
			}
		}
		if previous != nil && previous[0] != 42 {
			t.Fatal("received frame storage was reused")
		}
		frame.PCM[0] = 42
		previous = frame.PCM
	}
	in.Close()
	if frame, ok := readFrame(t, in); ok {
		t.Fatalf("partial frame emitted at Close: %+v", frame)
	}
	if d.closes.Load() != 1 {
		t.Fatalf("cleanup count = %d", d.closes.Load())
	}
}

func TestOverflowIsTerminalWithoutConsumer(t *testing.T) {
	d := &fakeDevice{}
	in := openFake(t, context.Background(), d)
	returned := make(chan struct{})
	go func() {
		d.emit(pcmBytes(make([]int16, frameSamples*(queueFrames+10))))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("callback blocked on full queue")
	}
	waitCleanup(t, in)
	for i := 0; i < queueFrames; i++ {
		frame, ok := readFrame(t, in)
		if !ok || frame.Err != nil || len(frame.PCM) != frameSamples {
			t.Fatalf("queued frame %d lost: %+v", i, frame)
		}
	}
	frame, ok := readFrame(t, in)
	if !ok || !errors.Is(frame.Err, ErrOverflow) || frame.PCM != nil {
		t.Fatalf("terminal overflow missing: %+v", frame)
	}
	if _, ok := readFrame(t, in); ok {
		t.Fatal("stream continued after overflow")
	}
	if d.closes.Load() != 1 {
		t.Fatalf("cleanup count = %d", d.closes.Load())
	}
}

func TestUnexpectedStopIsTerminal(t *testing.T) {
	d := &fakeDevice{}
	in := openFake(t, context.Background(), d)
	d.emit(pcmBytes(make([]int16, frameSamples)))
	d.stopped()
	waitCleanup(t, in)
	frame, _ := readFrame(t, in)
	if frame.Err != nil || len(frame.PCM) != frameSamples {
		t.Fatal("buffered capture was lost")
	}
	frame, _ = readFrame(t, in)
	if !errors.Is(frame.Err, ErrStopped) {
		t.Fatalf("stop error = %v", frame.Err)
	}
	if _, ok := readFrame(t, in); ok {
		t.Fatal("stream continued after stop")
	}
}

func TestCancellationCleansUpFullQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &fakeDevice{}
	in := openFake(t, ctx, d)
	d.emit(pcmBytes(make([]int16, frameSamples*queueFrames)))
	cancel()
	waitCleanup(t, in)
	for i := 0; i < queueFrames; i++ {
		frame, _ := readFrame(t, in)
		if frame.Err != nil {
			t.Fatalf("premature error: %v", frame.Err)
		}
	}
	frame, _ := readFrame(t, in)
	if !errors.Is(frame.Err, context.Canceled) {
		t.Fatalf("cancellation error = %v", frame.Err)
	}
	in.Close()
	if d.closes.Load() != 1 {
		t.Fatalf("cleanup count = %d", d.closes.Load())
	}
}

func TestConcurrentCloseWaitsForCallbacks(t *testing.T) {
	d := &fakeDevice{}
	in := openFake(t, context.Background(), d)
	var wg sync.WaitGroup
	wg.Add(17)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d.emit(pcmBytes(make([]int16, frameSamples)))
		}
	}()
	for i := 0; i < 16; i++ {
		go func() { defer wg.Done(); in.Close() }()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Close blocked")
	}
	if d.closes.Load() != 1 {
		t.Fatalf("cleanup count = %d", d.closes.Load())
	}
	for range in.Frames() {
	}
}

func TestOpenFailuresReleaseDevice(t *testing.T) {
	startErr := errors.New("start failed")
	for _, test := range []struct {
		name  string
		where string
	}{
		{"start error", "start"}, {"cancel during init", "init"}, {"cancel during start", "cancel-start"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &fakeDevice{}
			d.start = func() error {
				if test.where == "start" {
					return startErr
				}
				cancel()
				return nil
			}
			factory := func(data func([]byte), stopped func()) (captureDevice, error) {
				dev, err := d.factory(data, stopped)
				if test.where == "init" {
					cancel()
				}
				return dev, err
			}
			in, err := openWithDevice(ctx, factory)
			want := error(context.Canceled)
			if test.where == "start" {
				want = startErr
			}
			if in != nil || !errors.Is(err, want) {
				t.Fatalf("Open = %v, %v; want %v", in, err, want)
			}
			if d.closes.Load() != 1 {
				t.Fatalf("cleanup count = %d", d.closes.Load())
			}
		})
	}
}

func TestCanceledOpenNeverCreatesDevice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openWithDevice(ctx, func(func([]byte), func()) (captureDevice, error) {
		t.Fatal("created device for canceled context")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open error = %v", err)
	}
	if _, err := Open(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("public Open error = %v", err)
	}
}

func TestMalformedBufferIsTerminal(t *testing.T) {
	d := &fakeDevice{}
	in := openFake(t, context.Background(), d)
	d.emit([]byte{1})
	frame, ok := readFrame(t, in)
	if !ok || frame.Err == nil || frame.PCM != nil {
		t.Fatalf("malformed buffer error = %+v", frame)
	}
	waitCleanup(t, in)
}
