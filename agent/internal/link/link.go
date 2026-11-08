// Package link holds the WebSocket connection to the device.
package link

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coder/websocket"
)

type Conn struct {
	ws       *websocket.Conn
	incoming chan []byte
}

func Dial(ctx context.Context, addr string) (*Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		return nil, err
	}
	c := &Conn{ws: ws, incoming: make(chan []byte, 16)}
	// Reading keeps control frames flowing and surfaces device reports such as
	// screen_state. Callers that ignore Incoming lose nothing: excess frames drop.
	go c.readLoop()
	return c, nil
}

func (c *Conn) readLoop() {
	defer close(c.incoming)
	for {
		typ, b, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		select {
		case c.incoming <- b:
		default:
		}
	}
}

// Incoming yields text frames sent by the device; it closes when the connection ends.
func (c *Conn) Incoming() <-chan []byte { return c.incoming }

func (c *Conn) Send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

func (c *Conn) Close() { c.ws.Close(websocket.StatusNormalClosure, "bye") }
