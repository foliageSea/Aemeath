// Package link holds the WebSocket connection to the device.
package link

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"aemeath/agent/internal/netproxy"
	"github.com/coder/websocket"
)

type Conn struct {
	ws       *websocket.Conn
	inbox    *inbox
	readDone chan struct{}
}

func Dial(ctx context.Context, addr string) (*Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", &websocket.DialOptions{HTTPClient: &http.Client{Transport: netproxy.DirectTransport()}})
	if err != nil {
		return nil, err
	}
	c := &Conn{ws: ws, inbox: newInbox(), readDone: make(chan struct{})}
	// Keep control frames flowing even when callers do not consume Incoming.
	// The independent dispatcher retains each latest valid device state.
	go c.readLoop()
	return c, nil
}

func (c *Conn) readLoop() {
	defer close(c.readDone)
	defer c.inbox.stop()
	for {
		typ, b, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		c.inbox.enqueue(b)
	}
}

// Incoming yields device text frames until disconnect or Close. Valid v1 page_state
// and screen_state reports independently coalesce to their latest pending values;
// other frames are best-effort. Pending frames are discarded on disconnect.
func (c *Conn) Incoming() <-chan []byte { return c.inbox.incoming }

func (c *Conn) Send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

func (c *Conn) Close() {
	c.inbox.stop()
	c.ws.Close(websocket.StatusNormalClosure, "bye")
	<-c.readDone
	<-c.inbox.stopped
}
