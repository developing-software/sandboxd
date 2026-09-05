package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HeaderSize is the [u32 BE streamId] prefix on every binary frame. What follows is
// either PTY bytes for one sandbox or one proxied TCP connection's bytes — which of the
// two is decided when the stream is opened, never per frame.
const HeaderSize = 4

var ErrShortFrame = errors.New("wire: frame shorter than its header")

// Encode prefixes payload with its stream id.
func Encode(stream uint32, payload []byte) []byte {
	out := make([]byte, HeaderSize+len(payload))
	binary.BigEndian.PutUint32(out, stream)
	copy(out[HeaderSize:], payload)
	return out
}

// Decode splits a binary frame. The payload aliases buf — copy it before the read buffer
// is reused, which is what every caller that hands bytes to another goroutine must do.
func Decode(buf []byte) (stream uint32, payload []byte, err error) {
	if len(buf) < HeaderSize {
		return 0, nil, fmt.Errorf("%w: %d bytes", ErrShortFrame, len(buf))
	}
	return binary.BigEndian.Uint32(buf), buf[HeaderSize:], nil
}
