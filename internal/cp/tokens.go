package cp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// HMAC-signed capability tokens: base64url(json).base64url(hmac-sha256). There is no
// revocation list — a token is short-lived and scoped to one sandbox, and the only way to
// invalidate every one of them at once is to change the secret.

type TokenKind string

const (
	// TokenAttach opens one browser terminal; TokenPreview is traded for TokenPreviewCookie
	// on the first request to a preview host.
	TokenAttach        TokenKind = "attach"
	TokenPreview       TokenKind = "preview"
	TokenPreviewCookie TokenKind = "preview-cookie"
)

// TokenPayload is signed as-is. Field order is the JSON order and part of the format.
type TokenPayload struct {
	Kind TokenKind `json:"k"`
	SID  string    `json:"sid"`
	Port int       `json:"port,omitempty"`
	Exp  int64     `json:"exp"`
}

type Tokens struct {
	secret []byte
}

func NewTokens(secret string) *Tokens { return &Tokens{secret: []byte(secret)} }

// Sign stamps the expiry from ttl and returns the token.
func (t *Tokens) Sign(p TokenPayload, ttl time.Duration) string {
	p.Exp = time.Now().Add(ttl).UnixMilli()
	// A payload of plain strings and ints cannot fail to marshal.
	raw, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + base64.RawURLEncoding.EncodeToString(t.mac(body))
}

// Verify returns the payload only for an intact, unexpired token of the right kind. A
// caller checks the sid and port itself: the signature says the token is ours, not that
// it is for the sandbox in front of us.
func (t *Tokens) Verify(token string, kind TokenKind) (TokenPayload, bool) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return TokenPayload{}, false
	}
	given, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(given, t.mac(body)) {
		return TokenPayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return TokenPayload{}, false
	}
	var p TokenPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return TokenPayload{}, false
	}
	if p.Kind != kind || p.SID == "" || p.Exp < time.Now().UnixMilli() {
		return TokenPayload{}, false
	}
	return p, true
}

func (t *Tokens) mac(body string) []byte {
	h := hmac.New(sha256.New, t.secret)
	h.Write([]byte(body))
	return h.Sum(nil)
}
