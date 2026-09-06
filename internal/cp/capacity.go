package cp

import (
	"io"

	"sandboxd/internal/wire"
)

// Capacity is what a host reports about itself, live: it is never read from a row, and it
// is only knowable while the tunnel is up. The hub holds it, the scheduler bids with it,
// and the admin list shows it — which is why it lives here rather than in `store` or in
// either generated package.
type Capacity struct {
	Running int
	Max     int
}

// Free is the slot count placement bids with.
func (c Capacity) Free() int { return c.Max - c.Running }

// PTY is what a provider hands out for one attached terminal and what the attach bridge
// consumes: bytes both ways, a close, and a resize. Declared here for the same reason
// Capacity is — the fleet routes it between a provider and the bridge, and neither
// should have to name the other.
type PTY interface {
	io.ReadWriteCloser
	Resize(size wire.Size) error
}
