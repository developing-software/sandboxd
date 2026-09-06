package cp

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeHub stands in for the tunnel. The transport is a boundary we do not own, so a fake
// is the right tool; the store below is the real one, on :memory:.
type fakeHub struct {
	mu        sync.Mutex
	caps      map[string]Capacity
	created   []placed
	destroyed []string
	approved  []string
	refuse    bool
}

type placed struct {
	hostID string
	spec   wire.Spec
}

func newFakeHub() *fakeHub { return &fakeHub{caps: map[string]Capacity{}} }

func (f *fakeHub) Online(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.caps[id]
	return ok
}

func (f *fakeHub) Capacity(id string) (Capacity, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.caps[id]
	return c, ok
}

func (f *fakeHub) CreateSandbox(id string, spec wire.Spec) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.caps[id]
	if !ok || f.refuse {
		return false
	}
	c.Running++
	f.caps[id] = c
	f.created = append(f.created, placed{hostID: id, spec: spec})
	return true
}

func (f *fakeHub) DestroySandbox(_, sid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, sid)
}

func (f *fakeHub) NotifyApproved(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = append(f.approved, id)
}

func (f *fakeHub) setCapacity(id string, running, max int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps[id] = Capacity{Running: running, Max: max}
}

func (f *fakeHub) offline(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.caps, id)
}

func (f *fakeHub) placements() []placed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.created)
}

func (f *fakeHub) gone() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.destroyed)
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	store *store.SQLite
	hub   *fakeHub
	sched *Scheduler
	seq   int64
}

func newHarness(t *testing.T, sandboxEnv map[string]string) *harness {
	t.Helper()
	st, err := store.Open(":memory:", discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	hub := newFakeHub()
	events := make(chan Event, 16)
	sched := NewScheduler(st, hub, MostFreeSlots, sandboxEnv, events, discard())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go sched.Run(ctx)
	return &harness{t: t, ctx: ctx, store: st, hub: hub, sched: sched}
}

// host adds an approved, online host with the tags it reports.
func (h *harness) host(id string, max int, tags ...string) {
	h.t.Helper()
	err := h.store.InsertPendingHost(store.Host{
		ID: id, Name: id, Fingerprint: "fp-" + id, ApproveCode: "AAAA-AA",
		MaxSandboxes: max, Tags: tags,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.ApproveHost(id); err != nil {
		h.t.Fatal(err)
	}
	h.hub.setCapacity(id, 0, max)
}

// sandbox describes a row to submit. The scheduler is what writes it, so nothing here
// touches the store; created_at increases in call order, which is the queue order.
func (h *harness) sandbox(id string, env map[string]string, tags ...string) store.Sandbox {
	h.t.Helper()
	h.seq++
	return store.Sandbox{
		ID: id, OwnerID: "o", Image: "img", Env: env, Tags: tags,
		IdleTimeoutS: 60, CreatedAt: h.seq,
	}
}

func (h *harness) get(id string) store.Sandbox {
	h.t.Helper()
	sb, found, err := h.store.Sandbox(id)
	if err != nil || !found {
		h.t.Fatalf("sandbox %s: %v, %v", id, found, err)
	}
	return sb
}

// emit delivers a host event and waits for the loop to have applied it. Events and API
// calls share one channel, so a round trip after the event is proof it was handled.
func (h *harness) emit(e Event) {
	h.t.Helper()
	h.sched.events <- e
	if err := h.sched.do(h.ctx, func() error { return nil }); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) submit(sb store.Sandbox, secret map[string]string) {
	h.t.Helper()
	if err := h.sched.Submit(h.ctx, sb, secret); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) raw() string {
	h.t.Helper()
	dump, err := h.store.Dump("sandboxes")
	if err != nil {
		h.t.Fatal(err)
	}
	return dump
}

func TestPlacesOnTheHostWithMostFreeSlots(t *testing.T) {
	h := newHarness(t, nil)
	h.host("h1", 2)
	h.host("h2", 4)
	h.hub.setCapacity("h2", 3, 4) // h1 free = 2, h2 free = 1

	h.submit(h.sandbox("s_1", nil), map[string]string{"GIT_TOKEN": "shh"})

	got := h.hub.placements()
	if len(got) != 1 || got[0].hostID != "h1" {
		t.Fatalf("placed on %v", got)
	}
	if got[0].spec.SecretEnv["GIT_TOKEN"] != "shh" {
		t.Errorf("secret_env = %v", got[0].spec.SecretEnv)
	}
	if h.get("s_1").Status != store.Creating {
		t.Errorf("status = %s", h.get("s_1").Status)
	}

	// The one invariant worth reading a raw table for: no secret ever reaches a column.
	if raw := h.raw(); strings.Contains(raw, "shh") || strings.Contains(raw, "GIT_TOKEN") {
		t.Errorf("a secret reached the database:\n%s", raw)
	}

	h.emit(SandboxStarted{SID: "s_1"})
	if h.get("s_1").Status != store.Running {
		t.Errorf("status after started = %s", h.get("s_1").Status)
	}
}

func TestQueuesWhenFullAndDrainsOnEnd(t *testing.T) {
	h := newHarness(t, nil)
	h.host("h1", 1)
	h.submit(h.sandbox("s_1", nil), nil)
	h.submit(h.sandbox("s_2", nil), nil)
	h.submit(h.sandbox("s_3", nil), nil)

	if h.get("s_2").Status != store.Queued || h.get("s_3").Status != store.Queued {
		t.Fatal("a full host queues the rest")
	}
	h.hub.setCapacity("h1", 0, 1)
	h.emit(SandboxEnded{SID: "s_1", Reason: wire.EndClosed})

	if h.get("s_1").Status != store.Ended || h.get("s_1").EndedReason != wire.EndClosed {
		t.Errorf("s_1 = %+v", h.get("s_1"))
	}
	// Oldest first, and only as many as there is room for.
	if h.get("s_2").Status != store.Creating {
		t.Errorf("s_2 = %s, want the oldest queued one placed", h.get("s_2").Status)
	}
	if h.get("s_3").Status != store.Queued {
		t.Errorf("s_3 = %s", h.get("s_3").Status)
	}
}

func TestPendingAndOfflineHostsAreNotCandidates(t *testing.T) {
	h := newHarness(t, nil)
	// Connected, with room, but never approved.
	err := h.store.InsertPendingHost(store.Host{
		ID: "p", Name: "p", Fingerprint: "fp-p", ApproveCode: "X", MaxSandboxes: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.hub.setCapacity("p", 0, 9)
	// Approved, with room on paper, but not connected.
	h.host("off", 9)
	h.hub.offline("off")

	h.submit(h.sandbox("s_1", nil), nil)
	if h.get("s_1").Status != store.Queued {
		t.Errorf("status = %s, want queued", h.get("s_1").Status)
	}
}

func TestHostOnlineReconcilesBothDirections(t *testing.T) {
	h := newHarness(t, nil)
	h.host("h1", 5)
	h.submit(h.sandbox("s_1", nil), nil)
	h.submit(h.sandbox("s_2", nil), nil)
	h.emit(SandboxStarted{SID: "s_1"})
	h.emit(SandboxStarted{SID: "s_2"})
	if err := h.store.MarkActiveUnknown(); err != nil {
		t.Fatal(err)
	}

	// The host comes back reporting one of ours, and one we have never heard of.
	h.emit(HostOnline{HostID: "h1", Running: []string{"s_2", "s_orphan"}})

	if got := h.get("s_1"); got.Status != store.Ended || got.EndedReason != wire.EndLost {
		t.Errorf("unreported sandbox = %+v, want lost", got)
	}
	if got := h.get("s_2"); got.Status != store.Running || got.UnknownSince != nil {
		t.Errorf("reported sandbox = %+v, want running and no longer doubted", got)
	}
	// The other direction: a container the CP has no row for is reclaimed instead of
	// running forever.
	if got := h.hub.gone(); !slices.Equal(got, []string{"s_orphan"}) {
		t.Errorf("destroyed = %v, want the orphan reclaimed", got)
	}
}

func TestBootFailsQueuedAndDoubtsActive(t *testing.T) {
	h := newHarness(t, nil)
	h.host("h1", 1)
	h.submit(h.sandbox("s_running", nil), nil)
	h.emit(SandboxStarted{SID: "s_running"})
	// The host is full, so this one is still waiting on the secrets it was given.
	h.submit(h.sandbox("s_queued", nil), map[string]string{"GIT_TOKEN": "shh"})

	if err := h.sched.Boot(); err != nil {
		t.Fatal(err)
	}
	// Queued sandboxes lost the secrets they were waiting on, so they cannot be placed.
	got := h.get("s_queued")
	if got.Status != store.Ended || got.EndedReason != wire.EndFailed {
		t.Errorf("queued across a restart = %+v, want failed", got)
	}
	if !strings.Contains(got.EndedDetail, "secrets are not persisted") {
		t.Errorf("detail = %q", got.EndedDetail)
	}
	if h.get("s_running").UnknownSince == nil {
		t.Error("a running sandbox is doubted until its host re-reports it")
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t, nil)
	h.host("h1", 1)
	h.submit(h.sandbox("s_1", nil), nil)
	h.emit(SandboxStarted{SID: "s_1"})
	h.submit(h.sandbox("s_2", nil), nil)

	// Queued: nothing has been created anywhere, so it ends here.
	if err := h.sched.Cancel(h.ctx, "s_2"); err != nil {
		t.Fatal(err)
	}
	if h.get("s_2").Status != store.Ended {
		t.Errorf("cancelled queued = %s", h.get("s_2").Status)
	}

	// Running: the host is told, and the row ends when the host says it did.
	if err := h.sched.Cancel(h.ctx, "s_1"); err != nil {
		t.Fatal(err)
	}
	if got := h.hub.gone(); !slices.Equal(got, []string{"s_1"}) {
		t.Errorf("destroyed = %v", got)
	}
	if h.get("s_1").Status != store.Running {
		t.Errorf("cancelled running = %s, want still running until the host reports", h.get("s_1").Status)
	}
}

func TestCancelEndsASandboxWhoseHostIsGone(t *testing.T) {
	h := newHarness(t, nil)
	h.host("h1", 1)
	h.submit(h.sandbox("s_1", nil), nil)
	h.emit(SandboxStarted{SID: "s_1"})
	h.hub.offline("h1")

	if err := h.sched.Cancel(h.ctx, "s_1"); err != nil {
		t.Fatal(err)
	}
	got := h.get("s_1")
	if got.Status != store.Ended || got.EndedDetail != "host offline at close" {
		t.Errorf("= %+v, want ended here, since nobody is going to report it", got)
	}
}

func TestEnvPrecedence(t *testing.T) {
	operator := map[string]string{
		"LLM_API_KEY":  "op-key",
		"LLM_BASE_URL": "http://op",
		"HTTP_PROXY":   "http://proxy",
	}
	h := newHarness(t, operator)
	h.host("h1", 1)
	h.submit(
		h.sandbox("s_1", map[string]string{"LLM_BASE_URL": "http://sandbox"}),
		map[string]string{"LLM_API_KEY": "sandbox-key"},
	)

	spec := h.hub.placements()[0].spec
	// Highest first: the sandbox's secret_env, its env, then the operator's defaults.
	merged := maps.Clone(spec.Env)
	maps.Copy(merged, spec.SecretEnv)
	want := map[string]string{
		"LLM_BASE_URL": "http://sandbox",
		"LLM_API_KEY":  "sandbox-key",
		"HTTP_PROXY":   "http://proxy",
	}
	if !maps.Equal(merged, want) {
		t.Errorf("merged env = %v, want %v", merged, want)
	}
	if !maps.Equal(spec.Env, map[string]string{"LLM_BASE_URL": "http://sandbox"}) {
		t.Errorf("persisted env = %v, want only what the caller set", spec.Env)
	}
	// Operator env travels as secret_env because it may hold keys, and never lands in a row.
	if raw := h.raw(); strings.Contains(raw, "op-key") || strings.Contains(raw, "proxy") {
		t.Errorf("operator env reached the database:\n%s", raw)
	}
}

func TestCmdNullMeansTheImagesOwnEntry(t *testing.T) {
	h := newHarness(t, nil)
	h.host("h1", 2)

	cmd := []string{"python3", "-m", "http.server"}
	withCmd := h.sandbox("s_cmd", nil)
	withCmd.Cmd = cmd
	h.submit(withCmd, nil)
	h.submit(h.sandbox("s_default", nil), nil)

	got := h.hub.placements()
	if !slices.Equal(got[0].spec.Cmd, cmd) {
		t.Errorf("cmd = %v", got[0].spec.Cmd)
	}
	if got[1].spec.Cmd != nil {
		t.Errorf("cmd = %v, want nil so the worker runs the image's entry", got[1].spec.Cmd)
	}
}

func TestTagsFilterCandidates(t *testing.T) {
	h := newHarness(t, nil)
	h.host("docker", 4, "arch:amd64", "os:linux", "driver:docker")
	h.host("k8s", 4, "arch:arm64", "os:linux", "driver:kubernetes")

	h.submit(h.sandbox("s_k", nil, "driver:kubernetes"), nil)
	h.submit(h.sandbox("s_arm", nil, "arch:arm64", "os:linux"), nil)
	h.submit(h.sandbox("s_any", nil), nil)

	got := h.hub.placements()
	if len(got) != 3 {
		t.Fatalf("placed %d", len(got))
	}
	if got[0].hostID != "k8s" || got[1].hostID != "k8s" {
		t.Errorf("tagged sandboxes went to %s and %s", got[0].hostID, got[1].hostID)
	}
	// Untagged goes anywhere, and by then the k8s host has the fewer free slots.
	if got[2].hostID != "docker" {
		t.Errorf("untagged sandbox went to %s", got[2].hostID)
	}
}

func TestAnUnplaceableSandboxDoesNotBlockTheQueue(t *testing.T) {
	h := newHarness(t, nil)
	h.host("docker", 1, "driver:docker")

	// Oldest: needs a host that is not in the fleet right now.
	h.submit(h.sandbox("s_waiting", nil, "driver:kubernetes"), nil)
	h.submit(h.sandbox("s_next", nil, "driver:docker"), nil)

	// With tags the queue is no longer homogeneous, so strict head-of-line FIFO would
	// starve everything behind one requirement nothing can serve yet.
	if h.get("s_next").Status != store.Creating {
		t.Errorf("s_next = %s, want placed past the one that cannot be", h.get("s_next").Status)
	}
	if h.get("s_waiting").Status != store.Queued {
		t.Errorf("s_waiting = %s", h.get("s_waiting").Status)
	}
}
