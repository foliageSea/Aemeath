//go:build windows

package holdkey

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32             = syscall.NewLazyDLL("user32.dll")
	registerHotKey     = user32.NewProc("RegisterHotKey")
	unregisterHotKey   = user32.NewProc("UnregisterHotKey")
	getMessage         = user32.NewProc("GetMessageW")
	peekMessage        = user32.NewProc("PeekMessageW")
	postThreadMessage  = user32.NewProc("PostThreadMessageW")
	getAsyncKeyState   = user32.NewProc("GetAsyncKeyState")
	setTimer           = user32.NewProc("SetTimer")
	killTimer          = user32.NewProc("KillTimer")
	getCurrentThreadID = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")
)

const (
	vkF8        = 0x77
	hotkeyID    = 1
	modNoRepeat = 0x4000
	wmQuit      = 0x0012
	wmTimer     = 0x0113
	wmHotKey    = 0x0312
)

// winMessage matches MSG on both 32-bit and 64-bit Windows, including lPrivate.
type winMessage struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	x, y    int32
	private uint32
}

type windowsReady struct {
	threadID uintptr
	err      error
}

func start(callback func(bool), modified bool) (func(), error) {
	events := newTransitions(callback)
	ready := make(chan windowsReady, 1)
	quit := make(chan struct{})
	done := make(chan struct{})
	go windowsLoop(events, ready, quit, done, modified)
	result := <-ready
	if result.err != nil {
		<-done
		events.shutdown()
		return nil, result.err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			// The timer also wakes the loop if PostThreadMessage fails or it has exited.
			postThreadMessage.Call(result.threadID, wmQuit, 0, 0)
			<-done
			events.shutdown()
		})
	}, nil
}

func windowsLoop(events *transitions, ready chan<- windowsReady, quit <-chan struct{}, done chan<- struct{}, modified bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(done)
	threadID, _, _ := getCurrentThreadID.Call()
	var msg winMessage
	// Force creation of this thread's message queue before exposing its ID.
	peekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0)
	modifiers := uintptr(modNoRepeat)
	if modified {
		modifiers |= 0x0002 | 0x0004
	} // MOD_CONTROL | MOD_SHIFT
	ok, _, err := registerHotKey.Call(0, hotkeyID, modifiers, vkF8)
	if ok == 0 {
		ready <- windowsReady{err: fmt.Errorf("holdkey: reserve global F8 with RegisterHotKey (key may already be reserved): %w", err)}
		return
	}
	defer unregisterHotKey.Call(0, hotkeyID)
	// RegisterHotKey sends key-down only. A thread timer checks physical release
	// even while every application window is hidden; no UI message pump is needed.
	timer, _, err := setTimer.Call(0, 0, 10, 0)
	if timer == 0 {
		ready <- windowsReady{err: fmt.Errorf("holdkey: create F8 release timer: %w", err)}
		return
	}
	defer killTimer.Call(0, timer)
	defer events.change(false)
	ready <- windowsReady{threadID: threadID}
	down := false
	for {
		result, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) <= 0 {
			return
		}
		select {
		case <-quit:
			return
		default:
		}
		switch msg.message {
		case wmHotKey:
			if msg.wParam == hotkeyID {
				down = true
				events.change(true)
			}
		case wmTimer:
			if msg.wParam != timer {
				continue
			}
		default:
			continue
		}
		if down {
			state, _, _ := getAsyncKeyState.Call(vkF8)
			if state&0x8000 == 0 {
				down = false
				events.change(false)
			}
		}
	}
}
