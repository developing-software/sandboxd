package wire

import (
	"crypto/rand"
	"encoding/base32"
)

// Lowercase RFC 4648 base32, unpadded: an id has to survive as a DNS label, because the
// preview host is `<port>-<sid>.<domain>`.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// A human reads this one off a terminal and types it into a browser, so 0/O and 1/I are
// not in the alphabet. 32 divides 256, so the modulo below is unbiased.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// The prefixes are contract: `sid` and `s_` outlived the session → sandbox rename because
// ids are already minted and printed (DESIGN.md decision 5).
const (
	SandboxIDPrefix = "s_"
	HostIDPrefix    = "h_"
)

// RandomID returns n bytes of entropy in the id alphabet. Also the worker's own secret,
// at 32 bytes, which is why it takes a size.
func RandomID(n int) string { return encodeID(randomBytes(n)) }

func NewSandboxID() string { return SandboxIDPrefix + RandomID(16) }

func NewHostID() string { return HostIDPrefix + RandomID(16) }

// NewCode is the enrollment code the worker prints, e.g. K7QP-3M.
func NewCode() string { return encodeCode(randomBytes(6)) }

func encodeID(b []byte) string { return idEncoding.EncodeToString(b) }

// encodeCode formats as XXXX-XX; the dash is for the human, and approval compares the
// whole string including it.
func encodeCode(b []byte) string {
	out := make([]byte, 0, len(b)+1)
	for i, x := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(x)%len(codeAlphabet)])
	}
	return string(out)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	// crypto/rand.Read is documented never to fail since Go 1.24 — it panics instead of
	// returning an error, so there is nothing here to handle.
	_, _ = rand.Read(b)
	return b
}
