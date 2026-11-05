// Package link holds the WebSocket connection to the device.
package link

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coder/websocket"
)

type Conn struct {
	ws *websocket.Conn
}

func Dial(ctx context.Context, addr string) (*Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		return nil, err
	}
	// The device never sends data frames; this keeps control frames flowing
	// and cancels writes promptly if the device goes away.
	ws.CloseRead(context.Background())
	return &Conn{ws: ws}, nil
}

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
