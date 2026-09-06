package fleet

import (
	"context"
	"net"
	"slices"
	"testing"

	"sandboxd/internal/cp"
	"sandboxd/internal/wire"
)

// A provider that owns one host and records what reached it.
type fake struct {
	id       string
	calls    []string
	approved []string
}

func (f *fake) Online(id string) bool { return id == f.id }
func (f *fake) Capacity(id string) (cp.Capacity, bool) {
	return cp.Capacity{Running: 1, Max: 2}, id == f.id
}

func (f *fake) CreateSandbox(id string, spec wire.Spec) bool {
	f.calls = append(f.calls, "create "+spec.SID)
	return id == f.id
}
func (f *fake) DestroySandbox(_, sid string) { f.calls = append(f.calls, "destroy "+sid) }
func (f *fake) NotifyApproved(id string)     { f.approved = append(f.approved, id) }
func (f *fake) OpenPTY(_, sid string, _ wire.Size) (cp.PTY, error) {
	f.calls = append(f.calls, "pty "+sid)
	return nil, nil
}

func (f *fake) Dial(_ context.Context, _, sid string, _ int) (net.Conn, error) {
	f.calls = append(f.calls, "dial "+sid)
	return nil, nil
}

func TestRoutesByWhoOwnsTheHost(t *testing.T) {
	a, b := &fake{id: "h_a"}, &fake{id: "h_b"}
	f := New(a, b)

	if !f.Online("h_b") || f.Online("h_nope") {
		t.Error("online is whoever answers for the id")
	}
	if c, ok := f.Capacity("h_a"); !ok || c.Free() != 1 {
		t.Errorf("capacity = %+v, %v", c, ok)
	}
	if !f.CreateSandbox("h_b", wire.Spec{SID: "s_1"}) {
		t.Error("create on a live host")
	}
	f.DestroySandbox("h_b", "s_1")
	_, _ = f.OpenPTY("h_b", "s_1", wire.Size{})
	_, _ = f.Dial(t.Context(), "h_b", "s_1", 80)
	if want := []string{"create s_1", "destroy s_1", "pty s_1", "dial s_1"}; !slices.Equal(b.calls, want) {
		t.Errorf("b saw %v, want %v", b.calls, want)
	}
	if len(a.calls) != 0 {
		t.Errorf("a saw %v, want nothing", a.calls)
	}

	// Approval reaches everyone: the host is pending, so nobody owns it yet.
	f.NotifyApproved("h_new")
	if len(a.approved) != 1 || len(b.approved) != 1 {
		t.Error("approval must fan out")
	}
}

// Nothing here may panic on an id no provider knows: the scheduler asks about every
// approved row, including a worker that is offline.
func TestAnUnknownHostIsOfflineNotAPanic(t *testing.T) {
	f := New(&fake{id: "h_a"})
	if f.Online("h_x") || f.CreateSandbox("h_x", wire.Spec{}) {
		t.Error("unknown host is not online")
	}
	if _, ok := f.Capacity("h_x"); ok {
		t.Error("unknown host has no capacity")
	}
	f.DestroySandbox("h_x", "s")
	if _, err := f.OpenPTY("h_x", "s", wire.Size{}); err == nil {
		t.Error("pty on an unknown host must fail")
	}
	if _, err := f.Dial(t.Context(), "h_x", "s", 80); err == nil {
		t.Error("dial on an unknown host must fail")
	}
	if New().Online("h_a") {
		t.Error("an empty fleet has nobody online")
	}
}
