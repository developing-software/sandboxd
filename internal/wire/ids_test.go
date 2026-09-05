package wire

import (
	"strings"
	"testing"
)

type idsFixture struct {
	IDs []struct {
		Name    string `json:"name"`
		Bytes   string `json:"bytes"`
		Encoded string `json:"encoded"`
	} `json:"ids"`
	Codes []struct {
		Bytes string `json:"bytes"`
		Code  string `json:"code"`
	} `json:"codes"`
}

// Ids are random, so parity is about the encoding, not the value: the same bytes must
// produce the same string as the TypeScript implementation did.
func TestEncodingMatchesFrozenIDs(t *testing.T) {
	var fx idsFixture
	readFixture(t, "testdata/ids.json", &fx)
	if len(fx.IDs) == 0 || len(fx.Codes) == 0 {
		t.Fatal("no fixtures")
	}

	for _, c := range fx.IDs {
		if got := encodeID(decodeHex(t, c.Bytes)); got != c.Encoded {
			t.Errorf("%s: encodeID(%s) = %q, want %q", c.Name, c.Bytes, got, c.Encoded)
		}
	}
	for _, c := range fx.Codes {
		if got := encodeCode(decodeHex(t, c.Bytes)); got != c.Code {
			t.Errorf("encodeCode(%s) = %q, want %q", c.Bytes, got, c.Code)
		}
	}
}

func TestIDFormat(t *testing.T) {
	sid, host := NewSandboxID(), NewHostID()
	if !strings.HasPrefix(sid, "s_") || !strings.HasPrefix(host, "h_") {
		t.Fatalf("prefixes: %q %q", sid, host)
	}
	// 16 bytes of base32, unpadded, is 26 characters — and the whole id is one DNS label.
	if len(sid) != 2+26 || len(host) != 2+26 {
		t.Fatalf("lengths: %d %d", len(sid), len(host))
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	if i := strings.IndexFunc(sid[2:], func(r rune) bool {
		return !strings.ContainsRune(alphabet, r)
	}); i >= 0 {
		t.Errorf("%q is not in the id alphabet at %d", sid, i+2)
	}
}

func TestCodeFormat(t *testing.T) {
	code := NewCode()
	if len(code) != 7 || code[4] != '-' {
		t.Fatalf("NewCode() = %q, want XXXX-XX", code)
	}
	// The operator types this. Nothing that reads as another character belongs in it.
	if strings.ContainsAny(code, "0O1I") {
		t.Errorf("NewCode() = %q contains an ambiguous character", code)
	}
}

func TestIDsAreDistinct(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := NewSandboxID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}
