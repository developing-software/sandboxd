// Package sandbox runs sandboxes on one host: a driver creates the container, the manager
// owns every PTY, its ring buffer and its idle timer. Both daemons use it — the worker
// behind its tunnel, the control plane in-process as a provider — which is why it imports
// nothing of ours but `wire`. It is shared code, not a channel between the daemons: they
// still meet on the wire.
package sandbox

import (
	"context"
	"io"
	"net"

	"sandboxd/internal/wire"
)

// Driver is what actually runs a sandbox (DESIGN.md decision 14). Nothing above it may
// know it is Docker: Podman or Kubernetes is another package, not a branch in the manager.
// Dial is here rather than beside the tunnel because every driver has one and a provider
// inside the control plane has no tunnel to declare it on.
type Driver interface {
	// Create starts a container held idle, so a PTY can be exec'd into it later.
	Create(ctx context.Context, sid, image string) (id string, err error)
	Attach(ctx context.Context, id string, cmd []string, env map[string]string, size wire.Size) (PTY, error)
	// Dial opens a TCP connection to a port inside the container, for the preview proxy.
	Dial(ctx context.Context, id string, port int) (net.Conn, error)
	Destroy(ctx context.Context, id string) error
	// ListManaged finds, by label, every container this identity created: the orphan
	// sweep's input.
	ListManaged(ctx context.Context) ([]Managed, error)
}

// PTY is one exec'd process attached to a terminal. Read is its output, Write its input,
// Close ends it — which is the whole reason the port is worth doing: a blocking Read has
// no "bytes arrived before a handler was attached" race to work around.
type PTY interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, size wire.Size) error
}

// Managed is a container this identity created, found by label at start-up.
type Managed struct {
	ID  string
	SID string
}
