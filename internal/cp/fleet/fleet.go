// Package fleet is the seam between the control plane and where sandboxes run. The
// scheduler, the attach bridge, the preview proxy and the host admin each declare a small
// interface; this one type implements them all by asking each provider whether a host id
// is its own. A deployment with one provider can hand that provider to the scheduler
// directly — the interfaces are the same — and one with several hands it this.
package fleet

import (
	"context"
	"errors"
	"net"

	"sandboxd/internal/cp"
	"sandboxd/internal/wire"
)

// Provider is one place hosts come from: the tunnel hub, or a manager in this process.
// Every method takes the host id because a provider may hold many hosts; one that holds
// a single host compares.
type Provider interface {
	Online(hostID string) bool
	Capacity(hostID string) (cp.Capacity, bool)
	CreateSandbox(hostID string, spec wire.Spec) bool
	DestroySandbox(hostID, sid string)
	// NotifyApproved promotes a host that was waiting on an admin. Only the tunnel has
	// pending hosts; a provider with none does nothing.
	NotifyApproved(hostID string)
	OpenPTY(hostID, sid string, size wire.Size) (cp.PTY, error)
	Dial(ctx context.Context, hostID, sid string, port int) (net.Conn, error)
}

var errOffline = errors.New("fleet: host is offline")

type Fleet struct {
	providers []Provider
}

func New(providers ...Provider) *Fleet {
	return &Fleet{providers: providers}
}

// owner is the provider that has this host online, or nil. Two map lookups per call is
// the whole cost of not keeping a registry the providers would have to maintain.
func (f *Fleet) owner(hostID string) Provider {
	for _, p := range f.providers {
		if p.Online(hostID) {
			return p
		}
	}
	return nil
}

func (f *Fleet) Online(hostID string) bool { return f.owner(hostID) != nil }

func (f *Fleet) Capacity(hostID string) (cp.Capacity, bool) {
	if p := f.owner(hostID); p != nil {
		return p.Capacity(hostID)
	}
	return cp.Capacity{}, false
}

func (f *Fleet) CreateSandbox(hostID string, spec wire.Spec) bool {
	if p := f.owner(hostID); p != nil {
		return p.CreateSandbox(hostID, spec)
	}
	return false
}

func (f *Fleet) DestroySandbox(hostID, sid string) {
	if p := f.owner(hostID); p != nil {
		p.DestroySandbox(hostID, sid)
	}
}

// NotifyApproved reaches every provider: a pending host is not online, so nobody owns it
// yet, and the one that is holding its socket knows.
func (f *Fleet) NotifyApproved(hostID string) {
	for _, p := range f.providers {
		p.NotifyApproved(hostID)
	}
}

func (f *Fleet) OpenPTY(hostID, sid string, size wire.Size) (cp.PTY, error) {
	if p := f.owner(hostID); p != nil {
		return p.OpenPTY(hostID, sid, size)
	}
	return nil, errOffline
}

func (f *Fleet) Dial(ctx context.Context, hostID, sid string, port int) (net.Conn, error) {
	if p := f.owner(hostID); p != nil {
		return p.Dial(ctx, hostID, sid, port)
	}
	return nil, errOffline
}
