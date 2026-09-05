package worker

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"sandboxd/internal/wire"
	"sandboxd/internal/worker/driver"
)

func testSpec(t *testing.T) wire.Spec {
	t.Helper()
	return wire.Spec{
		SID:          "s_1",
		Image:        "sandbox:1",
		IdleTimeoutS: 60,
		Env:          map[string]string{"A": "1"},
		SecretEnv:    map[string]string{"S": "x"},
	}
}

type harness struct {
	mgr    *Manager
	drv    *fakeDriver
	events chan Event
	ctx    context.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	drv := newFakeDriver()
	events := make(chan Event, 32)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &harness{mgr: NewManager(ctx, drv, []string{"/entry"}, events, log), drv: drv, events: events, ctx: ctx}
}

func (h *harness) box(t *testing.T, sid string) *sandbox {
	t.Helper()
	h.mgr.mu.Lock()
	defer h.mgr.mu.Unlock()
	box, ok := h.mgr.boxes[sid]
	if !ok {
		t.Fatalf("no sandbox %s", sid)
	}
	return box
}

func (h *harness) event(t *testing.T) Event {
	t.Helper()
	select {
	case ev := <-h.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

func TestCreateRunsOneContainerAndDropsTheSecrets(t *testing.T) {
	h := newHarness(t)
	spec := testSpec(t)
	h.mgr.Create(h.ctx, spec)

	want := []string{
		"create c1",
		// The entry, the sandbox's env, its secrets, and the two reserved names.
		"attach c1 [/entry] [A S SANDBOXD_SESSION_ID TERM] 120x40",
	}
	if got := h.drv.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("driver calls\n got %v\nwant %v", got, want)
	}
	if ev := h.event(t); ev.Kind != Started || ev.SID != "s_1" {
		t.Fatalf("event = %+v, want started s_1", ev)
	}
	if id, ok := h.mgr.ContainerOf("s_1"); !ok || id != "c1" {
		t.Fatalf("ContainerOf = %q, %v", id, ok)
	}

	// The driver has them; the worker keeps no copy (DESIGN.md decision 7).
	box := h.box(t, "s_1")
	box.mu.Lock()
	kept := box.spec.SecretEnv
	box.mu.Unlock()
	if kept != nil {
		t.Errorf("secret_env survived the hand-off: %v", kept)
	}

	h.drv.reset()
	h.mgr.End(h.ctx, "s_1", wire.EndClosed, "")
	if got := h.drv.log(); !reflect.DeepEqual(got, []string{"destroy c1"}) {
		t.Errorf("driver calls on end = %v", got)
	}
	if ev := h.event(t); ev.Kind != Ended || ev.Reason != wire.EndClosed {
		t.Errorf("event = %+v, want ended closed", ev)
	}
	if h.mgr.Count() != 0 {
		t.Error("the sandbox should be gone")
	}
}

func TestCreateFailsWithNothingToCleanUp(t *testing.T) {
	h := newHarness(t)
	h.drv.failPull = "missing:1"
	spec := testSpec(t)
	spec.Image = "missing:1"

	h.mgr.Create(h.ctx, spec)

	ev := h.event(t)
	if ev.Kind != Ended || ev.Reason != wire.EndFailed || ev.Detail == "" {
		t.Fatalf("event = %+v, want ended failed with a detail", ev)
	}
	if got := h.drv.log(); len(got) != 0 {
		t.Errorf("nothing was created, so nothing should be destroyed: %v", got)
	}
	if h.mgr.Count() != 0 {
		t.Error("a failed sandbox should not be left reserved")
	}
}

func TestSpecCommandWinsAndADuplicateCreateIsIgnored(t *testing.T) {
	h := newHarness(t)
	spec := testSpec(t)
	spec.Cmd = []string{"bash", "-l"}

	h.mgr.Create(h.ctx, spec)
	h.mgr.Create(h.ctx, testSpec(t)) // same sid

	want := []string{"create c1", "attach c1 [bash -l] [A S SANDBOXD_SESSION_ID TERM] 120x40"}
	if got := h.drv.log(); !reflect.DeepEqual(got, want) {
		t.Errorf("driver calls\n got %v\nwant %v", got, want)
	}
}

func TestOutputReachesViewersAndInputReachesThePTY(t *testing.T) {
	h := newHarness(t)
	h.mgr.Create(h.ctx, testSpec(t))
	h.event(t) // started
	pty := h.drv.pty(t, "c1")

	pty.say(t, "before")
	fan := h.box(t, "s_1").fan
	waitFor(t, func() bool { return len(fan.ring.Snapshot()) == len("before") })

	v, replay, err := h.mgr.Attach("s_1", wire.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if string(replay) != "before" {
		t.Errorf("replay = %q, want what was printed before attaching", replay)
	}
	if got := <-pty.resizes; got != (wire.Size{Cols: 80, Rows: 24}) {
		t.Errorf("resize = %+v", got)
	}

	pty.say(t, "after")
	if got := string(<-v.Bytes()); got != "after" {
		t.Errorf("live = %q", got)
	}

	h.mgr.Write("s_1", []byte("typed"))
	if got := string(<-pty.input); got != "typed" {
		t.Errorf("input = %q", got)
	}
}

func TestTheProcessExitingEndsTheSandbox(t *testing.T) {
	h := newHarness(t)
	h.mgr.Create(h.ctx, testSpec(t))
	h.event(t) // started

	h.drv.pty(t, "c1").exit()

	if ev := h.event(t); ev.Kind != Ended || ev.Reason != wire.EndExited {
		t.Fatalf("event = %+v, want ended exited", ev)
	}
	if h.mgr.Count() != 0 {
		t.Error("the sandbox should be gone")
	}
}

func TestIdleSandboxesAreReaped(t *testing.T) {
	h := newHarness(t)
	never := testSpec(t)
	never.SID, never.IdleTimeoutS = "s_forever", 0
	timed := testSpec(t)
	timed.SID, timed.IdleTimeoutS = "s_timed", 1

	for _, spec := range []wire.Spec{never, timed} {
		h.mgr.Create(h.ctx, spec)
		h.event(t)
	}
	// Age both past a one-second timeout without waiting for one.
	for _, sid := range []string{"s_forever", "s_timed"} {
		fan := h.box(t, sid).fan
		fan.mu.Lock()
		fan.last = time.Now().Add(-2 * time.Second)
		fan.mu.Unlock()
	}

	h.mgr.reapIdle(h.ctx)

	if ev := h.event(t); ev.Kind != Ended || ev.SID != "s_timed" || ev.Reason != wire.EndIdle {
		t.Fatalf("event = %+v, want s_timed ended idle", ev)
	}
	// An idle timeout of zero means the sandbox is never reaped for being quiet.
	if h.mgr.Count() != 1 {
		t.Errorf("running = %v, want only s_forever", h.mgr.Running())
	}
}

// A keystroke resets the idle clock, which is what keeps a terminal someone is watching
// from being reaped mid-thought (DESIGN.md decision 10).
func TestInputPostponesTheReaper(t *testing.T) {
	h := newHarness(t)
	spec := testSpec(t)
	spec.IdleTimeoutS = 1
	h.mgr.Create(h.ctx, spec)
	h.event(t)

	fan := h.box(t, "s_1").fan
	fan.mu.Lock()
	fan.last = time.Now().Add(-2 * time.Second)
	fan.mu.Unlock()

	h.mgr.Write("s_1", []byte("x"))
	h.mgr.reapIdle(h.ctx)
	if h.mgr.Count() != 1 {
		t.Error("a sandbox written to should not be reaped")
	}
}

func TestSweepRemovesOrphansFromAPreviousRun(t *testing.T) {
	h := newHarness(t)
	h.drv.managed = []driver.Managed{{ID: "old1", SID: "s_a"}, {ID: "old2", SID: "s_b"}}

	if err := h.mgr.Sweep(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.drv.log(); !reflect.DeepEqual(got, []string{"destroy old1", "destroy old2"}) {
		t.Errorf("sweep = %v", got)
	}
}

func TestEndAllReportsEverySandboxAsLost(t *testing.T) {
	h := newHarness(t)
	for _, sid := range []string{"s_1", "s_2"} {
		spec := testSpec(t)
		spec.SID = sid
		h.mgr.Create(h.ctx, spec)
		h.event(t)
	}

	h.mgr.EndAll(h.ctx, wire.EndLost)
	for range 2 {
		if ev := h.event(t); ev.Kind != Ended || ev.Reason != wire.EndLost {
			t.Fatalf("event = %+v, want ended lost", ev)
		}
	}
	if h.mgr.Count() != 0 {
		t.Error("nothing should be left running")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
