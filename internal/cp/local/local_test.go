package local_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/fleet"
	"sandboxd/internal/cp/local"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/sandbox"
	"sandboxd/internal/sandbox/sandboxtest"
	"sandboxd/internal/wire"
)

const wait = 5 * time.Second

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

type harness struct {
	t      *testing.T
	ctx    context.Context
	store  *store.SQLite
	drv    *sandboxtest.Driver
	prov   *local.Provider
	events chan cp.Event
}

// managed is what a previous run left behind for the boot sweep to find.
func newHarness(t *testing.T, managed ...sandbox.Managed) *harness {
	t.Helper()
	st, err := store.Open(":memory:", discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	drv := sandboxtest.New()
	drv.Managed = managed
	events := make(chan cp.Event, 32)
	prov, err := local.New(ctx, drv, local.Options{
		Name: "cp", Tags: []string{"arch:test", "driver:fake", "gpu"}, Driver: "fake",
		Entry: []string{"/entry"}, Max: 2,
	}, st, events, discard())
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, ctx: ctx, store: st, drv: drv, prov: prov, events: events}
}

func (h *harness) run() {
	go h.prov.Run(h.ctx)
}

func (h *harness) event() cp.Event {
	h.t.Helper()
	select {
	case e := <-h.events:
		return e
	case <-time.After(wait):
		h.t.Fatal("no event")
		return nil
	}
}

func spec(sid string) wire.Spec {
	return wire.Spec{SID: sid, Image: "img", IdleTimeoutS: 60, Env: map[string]string{}, SecretEnv: map[string]string{"S": "x"}}
}

func TestBootUpsertsAnApprovedHostAndReportsOnline(t *testing.T) {
	h := newHarness(t, sandbox.Managed{ID: "old", SID: "s_old"})
	hosts, err := h.store.Hosts()
	if err != nil || len(hosts) != 1 {
		t.Fatalf("hosts = %v, %v", hosts, err)
	}
	host := hosts[0]
	if host.ID != h.prov.ID() || host.Status != store.HostApproved || host.Name != "cp" || host.MaxSandboxes != 2 {
		t.Errorf("row = %+v", host)
	}
	if want := []string{"arch:test", "driver:fake", "gpu", "provider:local"}; !slices.Equal(host.Tags, want) {
		t.Errorf("tags = %v, want %v", host.Tags, want)
	}
	if host.Fingerprint != local.Fingerprint("fake") {
		t.Error("the fingerprint is the driver's, not random: a restart must find the same row")
	}

	h.run()
	online, ok := h.event().(cp.HostOnline)
	if !ok || online.HostID != h.prov.ID() || len(online.Running) != 0 {
		t.Fatalf("first event = %#v, want host online with nothing running", online)
	}
	if got := h.drv.Calls(); !slices.Equal(got, []string{"destroy old"}) {
		t.Errorf("boot sweep = %v, want the orphan destroyed", got)
	}
	if !h.prov.Online(h.prov.ID()) || h.prov.Online("h_other") {
		t.Error("online is only for its own id")
	}
}

func TestASandboxStartsStreamsDialsAndEnds(t *testing.T) {
	h := newHarness(t)
	h.run()
	h.event() // online
	id := h.prov.ID()

	if !h.prov.CreateSandbox(id, spec("s_1")) {
		t.Fatal("create refused")
	}
	// Counted at once, so a second placement in the same drain sees one slot left.
	if c, _ := h.prov.Capacity(id); c.Running != 1 || c.Max != 2 {
		t.Errorf("capacity right after create = %+v", c)
	}
	if started, ok := h.event().(cp.SandboxStarted); !ok || started.SID != "s_1" {
		t.Fatalf("event = %#v, want started s_1", started)
	}
	fake := h.drv.PTY(t, "c1")

	// Replay, then live: what was printed before a viewer arrived comes first. A witness
	// viewer proves the bytes are in the ring before the viewer under test opens.
	witness, err := h.prov.OpenPTY(id, "s_1", wire.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	<-fake.Resizes
	fake.Say(t, "before")
	read(t, witness, len("before"))
	_ = witness.Close()

	term, err := h.prov.OpenPTY(id, "s_1", wire.Size{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-fake.Resizes; got != (wire.Size{Cols: 100, Rows: 30}) {
		t.Errorf("resize on open = %+v", got)
	}
	fake.Say(t, "after")
	if got := read(t, term, len("beforeafter")); got != "beforeafter" {
		t.Errorf("read %q, want the replay and then the live bytes", got)
	}
	if _, err := term.Write([]byte("typed")); err != nil {
		t.Fatal(err)
	}
	if got := string(<-fake.Input); got != "typed" {
		t.Errorf("input = %q", got)
	}
	if err := term.Resize(wire.Size{Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	if got := <-fake.Resizes; got != (wire.Size{Cols: 120, Rows: 40}) {
		t.Errorf("resize = %+v", got)
	}

	conn, err := h.prov.Dial(h.ctx, id, "s_1", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, conn, 4); got != "ping" {
		t.Errorf("echo = %q", got)
	}
	_ = conn.Close()

	h.prov.DestroySandbox(id, "s_1")
	ended, ok := h.event().(cp.SandboxEnded)
	if !ok || ended.SID != "s_1" || ended.Reason != wire.EndClosed {
		t.Fatalf("event = %#v, want ended closed", ended)
	}
	// The viewer learns the sandbox is gone by reading EOF, which is how the bridge
	// tells the browser.
	if _, err := term.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read after end = %v, want EOF", err)
	}
	if c, _ := h.prov.Capacity(id); c.Running != 0 {
		t.Errorf("capacity after end = %+v", c)
	}
	if _, err := h.prov.OpenPTY("h_other", "s_1", wire.Size{}); err == nil {
		t.Error("another host's id is not ours")
	}
}

// fakeWorkers stands in for the tunnel hub: one host, always online, with room.
type fakeWorkers struct {
	id     string
	placed []string
}

func (f *fakeWorkers) Online(id string) bool { return id == f.id }
func (f *fakeWorkers) Capacity(id string) (cp.Capacity, bool) {
	return cp.Capacity{Running: len(f.placed), Max: 4}, id == f.id
}

func (f *fakeWorkers) CreateSandbox(id string, spec wire.Spec) bool {
	if id != f.id {
		return false
	}
	f.placed = append(f.placed, spec.SID)
	return true
}
func (f *fakeWorkers) DestroySandbox(string, string)                     {}
func (f *fakeWorkers) NotifyApproved(string)                             {}
func (f *fakeWorkers) OpenPTY(string, string, wire.Size) (cp.PTY, error) { return nil, io.EOF }
func (f *fakeWorkers) Dial(context.Context, string, string, int) (net.Conn, error) {
	return nil, io.EOF
}

// The scheduler over a fleet of both providers: tags pick one, and with none the host
// with most free slots wins, whichever kind it is.
func TestTheSchedulerPlacesAcrossProviders(t *testing.T) {
	h := newHarness(t)
	worker := &fakeWorkers{id: "h_w"}
	err := h.store.InsertPendingHost(store.Host{
		ID: "h_w", Name: "box", Fingerprint: "fp-w", MaxSandboxes: 4,
		Tags: []string{"arch:test", "driver:docker", "provider:workers"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.ApproveHost("h_w"); err != nil {
		t.Fatal(err)
	}

	// A row placed on the provider before a restart: the provider comes back with nothing
	// running, and the scheduler must not wait on it.
	stale := store.Sandbox{ID: "s_stale", OwnerID: "o", Image: "img", Env: map[string]string{}, CreatedAt: 1}
	if err := h.store.InsertSandbox(stale); err != nil {
		t.Fatal(err)
	}
	if err := h.store.MarkCreating("s_stale", h.prov.ID()); err != nil {
		t.Fatal(err)
	}

	sched := cp.NewScheduler(h.store, fleet.New(worker, h.prov), cp.MostFreeSlots, nil, h.events, discard())
	if err := sched.Boot(); err != nil {
		t.Fatal(err)
	}
	go sched.Run(h.ctx)
	h.run()

	waitFor(t, func() bool { return status(t, h.store, "s_stale") == store.Ended })
	if sb, _, _ := h.store.Sandbox("s_stale"); sb.EndedReason != wire.EndLost {
		t.Errorf("stale row ended %q, want lost", sb.EndedReason)
	}

	submit := func(sid string, tags []string) {
		t.Helper()
		sb := store.Sandbox{ID: sid, OwnerID: "o", Image: "img", Env: map[string]string{}, Tags: tags, CreatedAt: time.Now().UnixMilli()}
		if err := sched.Submit(h.ctx, sb, map[string]string{"S": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	submit("s_local", []string{"provider:local"})
	submit("s_worker", []string{"provider:workers"})
	// No tags: the worker has 3 free, the provider 1, so the worker wins.
	submit("s_any", nil)

	for _, sid := range []string{"s_local", "s_worker", "s_any"} {
		waitFor(t, func() bool { return status(t, h.store, sid) != store.Queued })
	}
	if want := []string{"s_worker", "s_any"}; !slices.Equal(worker.placed, want) {
		t.Errorf("the worker got %v, want %v", worker.placed, want)
	}
	waitFor(t, func() bool { return status(t, h.store, "s_local") == store.Running })
	if sb, _, _ := h.store.Sandbox("s_local"); sb.HostID == nil || *sb.HostID != h.prov.ID() {
		t.Errorf("s_local placed on %v, want the provider", sb.HostID)
	}
	// And the secrets went to the driver, never to a row.
	dump, err := h.store.Dump("sandboxes")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, `"S"`) || strings.Contains(dump, "x\"") {
		t.Errorf("a secret reached the table:\n%s", dump)
	}
	if !slices.ContainsFunc(h.drv.Calls(), func(c string) bool { return strings.Contains(c, "[S ") }) {
		t.Errorf("the driver never saw the secret: %v", h.drv.Calls())
	}
}

func status(t *testing.T, st *store.SQLite, sid string) store.SandboxStatus {
	t.Helper()
	sb, found, err := st.Sandbox(sid)
	if err != nil || !found {
		t.Fatalf("sandbox %s: %v, %v", sid, found, err)
	}
	return sb.Status
}

func read(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
