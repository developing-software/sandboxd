// Package cp is the control plane: configuration, capability tokens, the scheduler and
// the sandbox use cases. Its subpackages hold the parts with a boundary of their own —
// `store` the SQL, `hosts` the tunnel, `attach` and `preview` the browser sockets, and
// `http` the JSON API.
//
// The dependency runs one way. This package never imports its own subpackages: it
// declares the narrow interfaces it needs and `cmd/sandboxd-api` supplies the
// implementations, which is what keeps the tree acyclic without a wiring framework.
package cp

import (
	"errors"
	"fmt"
)

// The failure kinds a use case can report. Only `cp/http` turns one into a status code —
// an HTTP status raised inside a use case is the bug this shape exists to prevent.
var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrInvalid       = errors.New("invalid request")
	ErrUnsatisfiable = errors.New("unsatisfiable")
)

// Error carries the prose a caller sees alongside the kind the API maps to a status. The
// kind is the wrapped error, so `errors.Is(err, cp.ErrNotFound)` is the whole test, while
// `err.Error()` stays the sentence and does not accumulate ": not found" suffixes.
type Error struct {
	kind error
	msg  string
}

func (e *Error) Error() string { return e.msg }
func (e *Error) Unwrap() error { return e.kind }

// NotFound is also the answer for a sandbox owned by somebody else: ownership failures
// and missing rows are deliberately indistinguishable (SPEC.md, "Auth").
func NotFound(what string) error { return &Error{ErrNotFound, what + " not found"} }

func Conflict(format string, args ...any) error {
	return &Error{ErrConflict, fmt.Sprintf(format, args...)}
}

func Invalid(format string, args ...any) error {
	return &Error{ErrInvalid, fmt.Sprintf(format, args...)}
}

// Unsatisfiable is the request that can never succeed rather than the one that must wait:
// no approved host carries the required tags, so queueing it would hide a typo forever.
func Unsatisfiable(format string, args ...any) error {
	return &Error{ErrUnsatisfiable, fmt.Sprintf(format, args...)}
}
