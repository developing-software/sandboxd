package server

import (
	"context"
	"crypto/subtle"
	"errors"

	"sandboxd/internal/gen/adminapi"
	"sandboxd/internal/gen/clientapi"
)

// The parent app's credential. Which routes it gates is stated in the two documents —
// `security: []` on the probe and on the terminal socket, the `Bearer` scheme everywhere
// else — so this file answers one question and does not also carry a list of paths.
//
// One token serves both documents today. Giving the operator surface its own is a second
// field on `Deps` and a different string compared in `adminAuth`; it is not a new
// middleware, and it is not a change to any handler (DESIGN.md decision 27).

// errBadToken is deliberately the same answer for a missing token and a wrong one.
var errBadToken = errors.New("unauthorized")

// clientAuth and adminAuth differ only in the generated types they are typed against: two
// documents mean two `SecurityHandler` interfaces with the same method name, so one struct
// cannot satisfy both.
type (
	clientAuth struct{ token string }
	adminAuth  struct{ token string }
)

func (a clientAuth) HandleBearer(
	ctx context.Context, _ clientapi.OperationName, t clientapi.Bearer,
) (context.Context, error) {
	return ctx, check(a.token, t.Token)
}

func (a adminAuth) HandleBearer(
	ctx context.Context, _ adminapi.OperationName, t adminapi.Bearer,
) (context.Context, error) {
	return ctx, check(a.token, t.Token)
}

// check compares in constant time: the token is a fixed secret compared on every request,
// so how long the comparison takes should not say how much of it was right.
func check(expected, given string) error {
	if subtle.ConstantTimeCompare([]byte(expected), []byte(given)) != 1 {
		return errBadToken
	}
	return nil
}

var (
	_ clientapi.SecurityHandler  = clientAuth{}
	_ adminapi.SecurityHandler = adminAuth{}
)
