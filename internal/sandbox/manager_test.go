package sandbox_test

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"sandboxd/internal/sandbox"
	"sandboxd/internal/sandbox/sandboxtest"
	"sandboxd/internal/wire"
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
	mgr    *sandbox.Manager
	drv    *sandboxtest.Driver
	events chan sandbox.Event
	ctx    context.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	drv := sandboxtest.New()
	events := make(chan sandbox.Event, 32)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := sandbox.NewManager(ctx, drv, []string{"/entry"}, 4, events, log)
	return &harness{mgr: mgr, drv: drv, events: events, ctx: ctx}
}

func (h *harness) event(t *testing.T) sandbox.Event {
	t.Helper()
	select {
	case ev := <-h.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return sandbox.Event{}
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
	if got := h.drv.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("driver calls\n got %v\nwant %v", got, want)
	}
	if ev := h.event(t); ev.Kind != sandbox.Started || ev.SID != "s_1" {
		t.Fatalf("event = %+v, want started s_1", ev)
	}
	if running, max := h.mgr.Capacity(); running != 1 || max != 4 {
		t.Fatalf("capacity = %d/%d", running, max)
	}

	// The driver has them; the manager keeps no copy (DESIGN.md decision 7).
	if h.mgr.SecretsKept("s_1") {
		t.Error("secret_env survived the hand-off")
	}

	h.drv.Reset()
	h.mgr.End(h.ctx, "s_1", wire.EndClosed, "")
	if got := h.drv.Calls(); !reflect.DeepEqual(got, []string{"destroy c1"}) {
		t.Errorf("driver calls on end = %v", got)
	}
	if ev := h.event(t); ev.Kind != sandbox.Ended || ev.Reason != wire.EndClosed {
		t.Errorf("event = %+v, want ended closed", ev)
	}
	if h.mgr.Count() != 0 {
		t.Error("the sandbox should be gone")
	}
}

func TestCreateFailsWithNothingToCleanUp(t *testing.T) {
	h := newHarness(t)
	h.drv.FailPull = "missing:1"
	spec := testSpec(t)
	spec.Image = "missing:1"

	h.mgr.Create(h.ctx, spec)

	ev := h.event(t)
	if ev.Kind != sandbox.Ended || ev.Reason != wire.EndFailed || ev.Detail == "" {
		t.Fatalf("event = %+v, want ended failed with a detail", ev)
	}
	if got := h.drv.Calls(); len(got) != 0 {
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
	if got := h.drv.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("driver calls\n got %v\nwant %v", got, want)
	}
}

func TestOutputReachesViewersAndInputReachesThePTY(t *testing.T) {
	h := newHarness(t)
	h.mgr.Create(h.ctx, testSpec(t))
	h.event(t) // started
	pty := h.drv.PTY(t, "c1")

	pty.Say(t, "before")
	waitFor(t, func() bool { return len(h.mgr.Tail("s_1")) == len("before") })

	v, replay, err := h.mgr.Attach("s_1", wire.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if string(replay) != "before" {
		t.Errorf("replay = %q, want what was printed before attaching", replay)
	}
	if got := <-pty.Resizes; got != (wire.Size{Cols: 80, Rows: 24}) {
		t.Errorf("resize = %+v", got)
	}

	pty.Say(t, "after")
	if got := string(<-v.Bytes()); got != "after" {
		t.Errorf("live = %q", got)
	}

	h.mgr.Write("s_1", []byte("typed"))
	if got := string(<-pty.Input); got != "typed" {
		t.Errorf("input = %q", got)
	}
}

func TestDialReachesAPortInsideARunningSandbox(t *testing.T) {
	h := newHarness(t)
	if _, err := h.mgr.Dial(h.ctx, "s_1", 8080); err == nil {
		t.Fatal("a sandbox that does not exist has nothing to dial")
	}
	h.mgr.Create(h.ctx, testSpec(t))
	h.event(t)

	conn, err := h.mgr.Dial(h.ctx, "s_1", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := h.drv.Calls()[2]; got != "dial c1:8080" {
		t.Errorf("dial went to %q", got)
	}
}

func TestTheProcessExitingEndsTheSandbox(t *testing.T) {
	h := newHarness(t)
	h.mgr.Create(h.ctx, testSpec(t))
	h.event(t) // started

	h.drv.PTY(t, "c1").Exit()

	if ev := h.event(t); ev.Kind != sandbox.Ended || ev.Reason != wire.EndExited {
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
		h.mgr.Backdate(sid, 2*time.Second)
	}

	h.mgr.ReapIdle(h.ctx)

	if ev := h.event(t); ev.Kind != sandbox.Ended || ev.SID != "s_timed" || ev.Reason != wire.EndIdle {
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

	h.mgr.Backdate("s_1", 2*time.Second)

	h.mgr.Write("s_1", []byte("x"))
	h.mgr.ReapIdle(h.ctx)
	if h.mgr.Count() != 1 {
		t.Error("a sandbox written to should not be reaped")
	}
}

func TestSweepRemovesOrphansFromAPreviousRun(t *testing.T) {
	h := newHarness(t)
	h.drv.Managed = []sandbox.Managed{{ID: "old1", SID: "s_a"}, {ID: "old2", SID: "s_b"}}

	if err := h.mgr.Sweep(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.drv.Calls(); !reflect.DeepEqual(got, []string{"destroy old1", "destroy old2"}) {
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
		if ev := h.event(t); ev.Kind != sandbox.Ended || ev.Reason != wire.EndLost {
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
