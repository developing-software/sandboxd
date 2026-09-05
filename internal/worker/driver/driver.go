// Package driver is the boundary to whatever actually runs a sandbox (DESIGN.md decision
// 14). Nothing above it may know it is Docker: Podman or Kubernetes is another type in
// here, not a branch in the worker.
//
// The interfaces the worker needs are declared by the worker, next to the code that calls
// them. What lives here is the concrete driver and the two types its signatures need.
package driver

import (
	"context"
	"io"

	"sandboxd/internal/wire"
)

// PTY is one exec'd process attached to a terminal. Read is its output, Write its input,
// Close ends it — which is the whole reason the port is worth doing: a blocking Read has
// no "bytes arrived before a handler was attached" race to work around.
type PTY interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, size wire.Size) error
}

// Managed is a container this worker identity created, found by label at start-up.
type Managed struct {
	ID  string
	SID string
}
