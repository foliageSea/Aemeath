package fishapi

import (
	"bytes"
	"context"
	"testing"

	"github.com/coder/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

func TestDecoderRejectsForgedLengthsDeepNestingAndWrongTypes(t *testing.T) {
	pack := func(v any) []byte {
		b, err := msgpack.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	forged := append([]byte{0x82, 0xa5}, []byte("event")...)
	forged = append(forged, 0xa5)
	forged = append(forged, []byte("audio")...)
	forged = append(forged, 0xa5)
	forged = append(forged, []byte("audio")...)
	forged = append(forged, 0xc6, 0xff, 0xff, 0xff, 0xff)
	deep := append([]byte{0x82, 0xa5}, []byte("event")...)
	deep = append(deep, 0xa6)
	deep = append(deep, []byte("future")...)
	deep = append(deep, 0xa4)
	deep = append(deep, []byte("meta")...)
	deep = append(deep, bytes.Repeat([]byte{0x91}, 18)...)
	deep = append(deep, 0xc0)
	valid := pack(map[string]any{"event": "audio", "audio": []byte{1, 0}})
	cases := map[string][]byte{
		"empty": nil, "declared4GiBbin": forged, "nested18": deep, "truncated": valid[:len(valid)-1], "trailing": append(append([]byte(nil), valid...), 0xc0),
		"textAudioNotBinary": pack(map[string]string{"event": "audio", "audio": "AAAA"}), "wrongEventType": pack(map[string]any{"event": []byte("audio")}),
		"hugeMapHeader": {0xdf, 0xff, 0xff, 0xff, 0xff}, "hugeArrayHeader": {0x91, 0xdd, 0xff, 0xff, 0xff, 0xff}, "oversizedMessage": make([]byte, maxMessageBytes+1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEvent(data); err == nil {
				t.Fatal("accepted malformed event")
			}
		})
	}
	// Ordinary extension fields use the mature decoder, with bounded recursive skips.
	data := pack(map[string]any{"event": "future", "metadata": map[string]any{"s": "value", "b": []byte{1}, "n": nil, "i": int64(-1), "f": 1.5, "a": []any{true, false, 1}}})
	if got, err := decodeEvent(data); err != nil || got.event != "future" {
		t.Fatalf("extension=%+v err=%v", got, err)
	}
}
func TestWebSocketRejectsWrongMessageKindAndOversizedMessage(t *testing.T) {
	for _, mode := range []string{"text", "oversized", "forged"} {
		t.Run(mode, func(t *testing.T) {
			c, calls := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
				if !consumeInput(t, ctx, conn) {
					return
				}
				kind := websocket.MessageBinary
				data := make([]byte, 0)
				switch mode {
				case "text":
					kind = websocket.MessageText
					data = []byte("raw-server-body test-secret")
				case "oversized":
					data = make([]byte, maxMessageBytes+1)
				case "forged":
					data = []byte{0xdf, 0xff, 0xff, 0xff, 0xff}
				}
				// Oversized messages may be cut off by the client's read limit while sending.
				_ = conn.Write(ctx, kind, data)
			})
			assertSafe(t, c.Stream(testContext(t), textInput("hello"), func([]int16) error { return nil }))
			if calls.Load() != 1 {
				t.Fatal("bad response retried")
			}
		})
	}
}
func TestInputUTF8ErrorClosesSessionWithoutRetry(t *testing.T) {
	for _, text := range []string{string([]byte{0xff}), string([]byte{0xe4})} {
		c, calls := mockFish(t, func(ctx context.Context, conn *websocket.Conn) {
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
			}
		})
		assertSafe(t, c.Stream(testContext(t), textInput(text), func([]int16) error { return nil }))
		if calls.Load() != 1 {
			t.Fatal("invalid input replayed")
		}
	}
}
