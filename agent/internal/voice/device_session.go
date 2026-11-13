package voice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"aemeath/agent/internal/netproxy"
	"github.com/coder/websocket"
)

// connectDevice owns protocol negotiation and the single device reader.
// The caller cancels s.ctx, closes s.ws and joins the reader on every exit.
func (s *session) connectDevice(address string) (<-chan incoming, <-chan struct{}, error) {
	endpoint, err := voiceURL(address)
	if err != nil {
		return nil, nil, err
	}
	dialCtx, dialCancel := context.WithTimeout(s.ctx, 5*time.Second)
	ws, _, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{HTTPClient: &http.Client{Transport: netproxy.DirectTransport()}})
	dialCancel()
	if err != nil {
		return nil, nil, errors.New("voice connection failed")
	}
	s.ws = ws
	ws.SetReadLimit(maxTextBytes)
	ready := false
	defer func() {
		if !ready {
			ws.CloseNow()
		}
	}()
	helloCtx, helloCancel := context.WithTimeout(s.ctx, 5*time.Second)
	typ, b, err := ws.Read(helloCtx)
	helloCancel()
	if err != nil {
		return nil, nil, errors.New("voice hello failed")
	}
	if typ != websocket.MessageText {
		return nil, nil, errors.New("expected voice hello")
	}
	hello, err := decodeMessage(b)
	if err != nil {
		return nil, nil, err
	}
	if hello.Type == "error" {
		return nil, nil, fmt.Errorf("device refused voice session: %s", s.safe(hello.Code))
	}
	if err = hello.validateHello(); err != nil {
		return nil, nil, err
	}
	if s.cfg.PlaybackOutput != "console" && s.cfg.TTSProvider == "fish" && !slices.Contains(hello.Capabilities, "stream_playback") {
		return nil, nil, errors.New("设备固件不支持 Fish 流式播放，请更新固件或选择 MiMo")
	}
	s.epoch = hello.Epoch
	rx := make(chan incoming, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			typ, b, err := ws.Read(s.ctx)
			select {
			case rx <- incoming{kind: typ, data: b, err: err}:
			case <-s.ctx.Done():
				return
			default:
				ws.CloseNow()
				select {
				case rx <- incoming{err: errors.New("voice receive queue overflow")}:
				case <-s.ctx.Done():
				}
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ready = true
	return rx, done, nil
}
