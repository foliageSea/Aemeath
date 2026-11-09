package fishapi

import (
	"bytes"
	"errors"
	"io"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

type receivedEvent struct {
	event, reason string
	audio         []byte
}

var errEncoding = errors.New("invalid bounded MessagePack")

// Decode only typed fields. Explicit wire-length checks precede allocations:
// a forged bin32 length cannot turn a small WebSocket frame into a huge allocation.
func decodeEvent(data []byte) (receivedEvent, error) {
	var out receivedEvent
	if len(data) == 0 || len(data) > maxMessageBytes {
		return out, errEncoding
	}
	reader := bytes.NewReader(data)
	dec := msgpack.NewDecoder(reader)
	count, err := dec.DecodeMapLen()
	if err != nil || count < 1 || count > 32 {
		return out, errEncoding
	}
	seen := map[string]bool{}
	budget := 4096
	for i := 0; i < count; i++ {
		key, err := decodeBoundedBytes(dec, reader, 64, false)
		if err != nil {
			return out, errEncoding
		}
		name := string(key)
		if seen[name] {
			return out, errEncoding
		}
		seen[name] = true
		switch name {
		case "event", "reason":
			b, err := decodeBoundedBytes(dec, reader, 64, false)
			if err != nil {
				return out, errEncoding
			}
			if name == "event" {
				out.event = string(b)
			} else {
				out.reason = string(b)
			}
		case "audio":
			out.audio, err = decodeBoundedBytes(dec, reader, maxMessageBytes, true)
			if err != nil {
				return out, errEncoding
			}
		default:
			if skipBounded(dec, reader, 0, &budget) != nil {
				return out, errEncoding
			}
		}
	}
	if out.event == "" || reader.Len() != 0 {
		return out, errEncoding
	}
	return out, nil
}
func decodeBoundedBytes(dec *msgpack.Decoder, r *bytes.Reader, limit int, binary bool) ([]byte, error) {
	code, err := dec.PeekCode()
	if err != nil {
		return nil, err
	}
	isBin := code == msgpcode.Bin8 || code == msgpcode.Bin16 || code == msgpcode.Bin32
	isString := msgpcode.IsFixedString(code) || code == msgpcode.Str8 || code == msgpcode.Str16 || code == msgpcode.Str32
	if (binary && !isBin) || (!binary && !isString) {
		return nil, errEncoding
	}
	n, err := dec.DecodeBytesLen()
	if err != nil || n < 0 || n > limit || n > r.Len() {
		return nil, errEncoding
	}
	b := make([]byte, n)
	if dec.ReadFull(b) != nil {
		return nil, errEncoding
	}
	return b, nil
}
func skipBounded(dec *msgpack.Decoder, r *bytes.Reader, depth int, budget *int) error {
	if depth > 16 || *budget <= 0 {
		return errEncoding
	}
	*budget--
	code, err := dec.PeekCode()
	if err != nil {
		return err
	}
	switch {
	case msgpcode.IsFixedString(code) || code == msgpcode.Str8 || code == msgpcode.Str16 || code == msgpcode.Str32 || code == msgpcode.Bin8 || code == msgpcode.Bin16 || code == msgpcode.Bin32:
		n, err := dec.DecodeBytesLen()
		if err != nil || n < 0 || n > r.Len() {
			return errEncoding
		}
		_, err = io.CopyN(io.Discard, dec.Buffered(), int64(n))
		return err
	case msgpcode.IsFixedMap(code) || code == msgpcode.Map16 || code == msgpcode.Map32:
		n, err := dec.DecodeMapLen()
		if err != nil || n < 0 || n > *budget/2 {
			return errEncoding
		}
		for i := 0; i < n*2; i++ {
			if err = skipBounded(dec, r, depth+1, budget); err != nil {
				return err
			}
		}
		return nil
	case msgpcode.IsFixedArray(code) || code == msgpcode.Array16 || code == msgpcode.Array32:
		n, err := dec.DecodeArrayLen()
		if err != nil || n < 0 || n > *budget {
			return errEncoding
		}
		for i := 0; i < n; i++ {
			if err = skipBounded(dec, r, depth+1, budget); err != nil {
				return err
			}
		}
		return nil
	case code == msgpcode.Nil:
		return dec.DecodeNil()
	case code == msgpcode.True || code == msgpcode.False:
		_, err = dec.DecodeBool()
		return err
	case code == msgpcode.Float || code == msgpcode.Double:
		_, err = dec.DecodeFloat64()
		return err
	case msgpcode.IsFixedNum(code) || code == msgpcode.Uint8 || code == msgpcode.Uint16 || code == msgpcode.Uint32 || code == msgpcode.Uint64 || code == msgpcode.Int8 || code == msgpcode.Int16 || code == msgpcode.Int32 || code == msgpcode.Int64:
		_, err = dec.DecodeInt64()
		return err
	default:
		return errEncoding
	}
}
