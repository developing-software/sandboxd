package wire

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// The fixtures are frames from the first implementation, frozen: a worker from an older
// release must still be understood, so a change that fails them is a wire break.
type framingFixture struct {
	Cases []struct {
		Name    string `json:"name"`
		Stream  uint32 `json:"stream"`
		Payload string `json:"payload"`
		Encoded string `json:"encoded"`
	} `json:"cases"`
}

func TestEncodeMatchesFrozenBytes(t *testing.T) {
	var fx framingFixture
	readFixture(t, "testdata/framing.json", &fx)
	if len(fx.Cases) == 0 {
		t.Fatal("no fixtures")
	}

	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			payload := decodeHex(t, c.Payload)
			want := decodeHex(t, c.Encoded)

			got := Encode(c.Stream, payload)
			if !bytes.Equal(got, want) {
				t.Fatalf("Encode(%d, %x)\n got %x\nwant %x", c.Stream, payload, got, want)
			}

			stream, back, err := Decode(want)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if stream != c.Stream {
				t.Errorf("stream = %d, want %d", stream, c.Stream)
			}
			if !bytes.Equal(back, payload) {
				t.Errorf("payload = %x, want %x", back, payload)
			}
		})
	}
}

func TestDecodeShortFrame(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		if _, _, err := Decode(make([]byte, n)); !errors.Is(err, ErrShortFrame) {
			t.Errorf("Decode(%d bytes) error = %v, want ErrShortFrame", n, err)
		}
	}
	// Exactly a header is a legal empty frame, not an error: a zero-length write on a
	// proxied socket is meaningful to nobody, but it must not tear the tunnel down.
	if _, payload, err := Decode(make([]byte, HeaderSize)); err != nil || len(payload) != 0 {
		t.Errorf("Decode(header only) = %v, %v", payload, err)
	}
}

func TestDecodeAliasesItsBuffer(t *testing.T) {
	buf := Encode(1, []byte("abc"))
	_, payload, err := Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	// Documented behaviour, and the reason every caller handing bytes to another
	// goroutine copies first.
	buf[HeaderSize] = 'z'
	if payload[0] != 'z' {
		t.Error("payload should alias buf")
	}
}

func readFixture(t *testing.T, path string, into any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatal(err)
	}
}

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
