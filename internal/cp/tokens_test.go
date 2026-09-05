package cp

import (
	"strings"
	"testing"
	"time"
)

func TestTokensSignAndVerifyByKind(t *testing.T) {
	tk := NewTokens("secret")
	token := tk.Sign(TokenPayload{Kind: TokenAttach, SID: "s_abc"}, time.Second)

	p, ok := tk.Verify(token, TokenAttach)
	if !ok || p.SID != "s_abc" {
		t.Fatalf("verify = %+v, %v", p, ok)
	}
	// The kind is part of what the signature means: a preview cookie is not an attach token.
	if _, ok := tk.Verify(token, TokenPreview); ok {
		t.Error("a token verified as the wrong kind")
	}
}

func TestTokensReject(t *testing.T) {
	tk := NewTokens("secret")
	token := tk.Sign(TokenPayload{Kind: TokenPreview, SID: "s_abc", Port: 3000}, time.Second)
	body, sig, _ := strings.Cut(token, ".")

	cases := map[string]struct {
		tokens *Tokens
		token  string
	}{
		"another secret":  {NewTokens("other"), token},
		"tampered body":   {tk, body + "x." + sig},
		"tampered sig":    {tk, body + "." + flipLast(sig)},
		"no signature":    {tk, body},
		"empty":           {tk, ""},
		"garbage":         {tk, "garbage"},
		"already expired": {tk, tk.Sign(TokenPayload{Kind: TokenPreview, SID: "s_abc"}, -time.Second)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := c.tokens.Verify(c.token, TokenPreview); ok {
				t.Error("accepted")
			}
		})
	}
}

// flipLast changes the final character to a different one, so a signature is always
// altered — picking a fixed letter would leave it untouched one time in sixty-four.
func flipLast(s string) string {
	if s[len(s)-1] == 'A' {
		return s[:len(s)-1] + "B"
	}
	return s[:len(s)-1] + "A"
}

func TestTokensCarryThePort(t *testing.T) {
	tk := NewTokens("k")
	token := tk.Sign(TokenPayload{Kind: TokenPreviewCookie, SID: "s_1", Port: 8888}, time.Minute)
	p, ok := tk.Verify(token, TokenPreviewCookie)
	if !ok || p.Port != 8888 {
		t.Errorf("verify = %+v, %v", p, ok)
	}
	// The signature says the token is ours, not that it is for the sandbox in front of us:
	// the caller compares sid and port itself, which is why they must survive the round trip.
	if p.SID != "s_1" {
		t.Errorf("sid = %q", p.SID)
	}
}
