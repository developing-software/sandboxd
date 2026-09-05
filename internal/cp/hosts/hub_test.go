package hosts

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// A real WebSocket against a real hub, driven by a worker that is only as real as the
// wire: the transport is a boundary we do not own, so this is the one fake here.

const wait = 5 * time.Second

type hubHarness struct {
	t      *testing.T
	ctx    context.Context
	store  *store.SQLite
	hub    *Hub
	events chan cp.Event
	url    string
}

func newHubHarness(t *testing.T, joinToken string) *hubHarness {
	t.Helper()
	s := openStore(t)
	events := make(chan cp.Event, 32)
	hub := NewHub(s, joinToken, events, discard())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /tunnel", hub.Serve)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &hubHarness{
		t: t, ctx: t.Context(), store: s, hub: hub, events: events,
		url: "ws" + strings.TrimPrefix(srv.URL, "http") + "/tunnel",
	}
}

// event returns the next thing the hub told the scheduler.
func (h *hubHarness) event() cp.Event {
	h.t.Helper()
	select {
	case e := <-h.events:
		return e
	case <-time.After(wait):
		h.t.Fatal("no event")
		return nil
	}
}

type worker struct {
	t   *testing.T
	ctx context.Context
	ws  *websocket.Conn
}

func (h *hubHarness) worker() *worker {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, wait)
	h.t.Cleanup(cancel)
	// coder/websocket documents that the dial response body needs no closing by the
	// caller, so bodyclose's report here is a false positive.
	ws, _, err := websocket.Dial(ctx, h.url, nil) //nolint:bodyclose
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = ws.CloseNow() })
	return &worker{t: h.t, ctx: ctx, ws: ws}
}

func (w *worker) send(m wire.FromHost) {
	w.t.Helper()
	b, err := wire.Marshal(m)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.ws.Write(w.ctx, websocket.MessageText, b); err != nil {
		w.t.Fatal(err)
	}
}

func (w *worker) sendFrame(stream uint32, payload []byte) {
	w.t.Helper()
	if err := w.ws.Write(w.ctx, websocket.MessageBinary, wire.Encode(stream, payload)); err != nil {
		w.t.Fatal(err)
	}
}

// control reads until the next JSON message from the control plane.
func (w *worker) control() wire.FromCP {
	w.t.Helper()
	for {
		typ, data, err := w.ws.Read(w.ctx)
		if err != nil {
			w.t.Fatalf("read: %v", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		m, err := wire.ParseFromCP(data)
		if err != nil {
			w.t.Fatal(err)
		}
		return m
	}
}

// frame reads until the next binary frame.
func (w *worker) frame() (uint32, []byte) {
	w.t.Helper()
	for {
		typ, data, err := w.ws.Read(w.ctx)
		if err != nil {
			w.t.Fatalf("read: %v", err)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		stream, payload, err := wire.Decode(data)
		if err != nil {
			w.t.Fatal(err)
		}
		return stream, payload
	}
}

// accepted brings a worker all the way to hello.ok using the operator's join token, which
// is the shortest honest path to a connected host.
func (h *hubHarness) accepted(fingerprint string, running ...string) (*worker, string) {
	h.t.Helper()
	w := h.worker()
	w.send(hello(fingerprint, joining("jt"), func(m *wire.Hello) {
		m.Running = running
		m.MaxSandboxes = 4
	}))
	ok, is := w.control().(*wire.HelloOK)
	if !is {
		h.t.Fatalf("expected hello.ok")
	}
	if _, is := h.event().(cp.HostOnline); !is {
		h.t.Fatal("accepting a host tells the scheduler what it is running")
	}
	return w, ok.HostID
}

func TestAPendingHostIsPromotedOnTheSocketItIsHolding(t *testing.T) {
	h := newHubHarness(t, "")
	w := h.worker()
	w.send(hello("fp1"))

	got, is := w.control().(*wire.HelloPending)
	if !is || !codeFormat.MatchString(got.Code) {
		t.Fatalf("first answer = %+v", got)
	}
	if h.hub.Online(got.HostID) {
		t.Error("a pending host is not placeable")
	}

	// An admin approves: the worker learns without reconnecting.
	if err := h.store.ApproveHost(got.HostID); err != nil {
		t.Fatal(err)
	}
	h.hub.NotifyApproved(got.HostID)

	ok, is := w.control().(*wire.HelloOK)
	if !is || ok.HostID != got.HostID {
		t.Fatalf("after approval = %+v", ok)
	}
	if !h.hub.Online(got.HostID) {
		t.Error("an accepted host is online")
	}
	if _, is := h.event().(cp.HostOnline); !is {
		t.Error("the scheduler is told")
	}
}

func TestARevokedHostIsRejectedAndHungUpOn(t *testing.T) {
	h := newHubHarness(t, "jt")
	_, hostID := h.accepted("fp1")
	if err := h.store.RevokeHost(hostID); err != nil {
		t.Fatal(err)
	}

	w := h.worker()
	w.send(hello("fp1", joining("jt")))
	got, is := w.control().(*wire.HelloRejected)
	if !is || got.Reason != "revoked" {
		t.Fatalf("= %+v", got)
	}
	// And the reason actually reaches the worker before the socket goes.
	if _, _, err := w.ws.Read(w.ctx); err == nil {
		t.Error("the socket stays open after a rejection")
	}
}

func TestHeartbeatUpdatesCapacity(t *testing.T) {
	h := newHubHarness(t, "jt")
	w, hostID := h.accepted("fp1")

	if got, ok := h.hub.Capacity(hostID); !ok || got != (cp.Capacity{Running: 0, Max: 4}) {
		t.Fatalf("capacity from hello = %+v, %v", got, ok)
	}
	w.send(&wire.Heartbeat{Running: 2, Max: 5})
	if _, is := h.event().(cp.Heartbeat); !is {
		t.Fatal("a heartbeat is what drains the queue")
	}
	if got, _ := h.hub.Capacity(hostID); got != (cp.Capacity{Running: 2, Max: 5}) {
		t.Errorf("capacity = %+v", got)
	}
	row, _, _ := h.store.Host(hostID)
	if row.MaxSandboxes != 5 || row.LastSeenAt == nil {
		t.Errorf("row = %+v, want the capacity written through", row)
	}
}

func TestSandboxEventsReachTheScheduler(t *testing.T) {
	h := newHubHarness(t, "jt")
	w, _ := h.accepted("fp1")

	w.send(&wire.SandboxStarted{SID: "s_1"})
	if got, is := h.event().(cp.SandboxStarted); !is || got.SID != "s_1" {
		t.Errorf("= %+v", got)
	}
	w.send(&wire.SandboxEnded{SID: "s_1", Reason: wire.EndIdle, Detail: "no traffic"})
	got, is := h.event().(cp.SandboxEnded)
	if !is || got.Reason != wire.EndIdle || got.Detail != "no traffic" {
		t.Errorf("= %+v", got)
	}
}

func TestPTYStreams(t *testing.T) {
	h := newHubHarness(t, "jt")
	w, hostID := h.accepted("fp1")

	first, err := h.hub.OpenPTY(hostID, "s_1", wire.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.hub.OpenPTY(hostID, "s_2", wire.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	// Control-plane stream ids are odd and never reused within a connection.
	a, is := w.control().(*wire.PtyOpen)
	if !is || a.Stream != 1 || a.SID != "s_1" {
		t.Fatalf("first pty.open = %+v", a)
	}
	if b, is := w.control().(*wire.PtyOpen); !is || b.Stream != 3 {
		t.Fatalf("second pty.open = %+v", b)
	}

	// Browser to sandbox.
	if _, err := first.Write([]byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	stream, payload := w.frame()
	if stream != 1 || string(payload) != "ls\n" {
		t.Errorf("frame = %d %q", stream, payload)
	}

	// Sandbox to browser, routed by id: the other stream must not see it.
	w.sendFrame(1, []byte("out"))
	if got := read(t, first, 3); got != "out" {
		t.Errorf("read %q", got)
	}

	if err := first.Resize(wire.Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	if got, is := w.control().(*wire.PtyResize); !is || got.SID != "s_1" || got.Size.Cols != 100 {
		t.Errorf("resize = %+v", got)
	}

	// The sandbox's terminal ends: the viewer sees EOF, and closing after that says
	// nothing more to the host.
	w.send(&wire.PtyClosed{Stream: 1})
	if _, err := io.ReadAll(first); err != nil {
		t.Errorf("read after pty.closed = %v, want a clean EOF", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// The viewer of the second stream leaves of its own accord, and that one is told.
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if got, is := w.control().(*wire.PtyClose); !is || got.Stream != 3 {
		t.Errorf("pty.close = %+v, want only the stream that was still open", got)
	}
}

func TestPortDial(t *testing.T) {
	h := newHubHarness(t, "jt")
	w, hostID := h.accepted("fp1")

	type dialed struct {
		conn net.Conn
		err  error
	}
	open := make(chan dialed, 1)
	go func() {
		c, err := h.hub.Dial(h.ctx, hostID, "s_1", 3000)
		open <- dialed{c, err}
	}()

	got, is := w.control().(*wire.PortDial)
	if !is || got.Port != 3000 || got.Stream != 1 {
		t.Fatalf("port.dial = %+v", got)
	}
	// A dial resolves only when the sandbox says the socket is up.
	select {
	case d := <-open:
		t.Fatalf("dial returned before port.open: %+v", d)
	case <-time.After(50 * time.Millisecond):
	}
	w.send(&wire.PortOpen{Stream: 1})

	d := <-open
	if d.err != nil {
		t.Fatal(d.err)
	}
	if _, err := d.conn.Write([]byte("GET / HTTP/1.1\r\n")); err != nil {
		t.Fatal(err)
	}
	stream, payload := w.frame()
	if stream != 1 || !strings.HasPrefix(string(payload), "GET /") {
		t.Errorf("frame = %d %q", stream, payload)
	}
	w.sendFrame(1, []byte("HTTP/1.1 200 OK"))
	if got := read(t, d.conn, 15); got != "HTTP/1.1 200 OK" {
		t.Errorf("read %q", got)
	}

	if err := d.conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got, is := w.control().(*wire.PortClose); !is || got.Stream != 1 {
		t.Errorf("port.close = %+v", got)
	}
}

func TestPortDialFailsWithTheSandboxsReason(t *testing.T) {
	h := newHubHarness(t, "jt")
	w, hostID := h.accepted("fp1")

	errs := make(chan error, 1)
	go func() {
		_, err := h.hub.Dial(h.ctx, hostID, "s_1", 3000)
		errs <- err
	}()
	if _, is := w.control().(*wire.PortDial); !is {
		t.Fatal("expected port.dial")
	}
	w.send(&wire.PortError{Stream: 1, Msg: "connection refused"})

	err := <-errs
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the sandbox's own reason", err)
	}
}

func TestAHostThatGoesAwayEndsItsStreams(t *testing.T) {
	h := newHubHarness(t, "jt")
	w, hostID := h.accepted("fp1")
	pty, err := h.hub.OpenPTY(hostID, "s_1", wire.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if _, is := w.control().(*wire.PtyOpen); !is {
		t.Fatal("expected pty.open")
	}

	if err := w.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	// The viewer learns, rather than waiting on a socket nobody is holding.
	buf := make([]byte, 8)
	if _, err := pty.Read(buf); !errors.Is(err, io.EOF) {
		t.Errorf("read = %v, want EOF", err)
	}
	waitFor(t, func() bool { return !h.hub.Online(hostID) })
	if _, err := h.hub.OpenPTY(hostID, "s_1", wire.Size{Cols: 80, Rows: 24}); err == nil {
		t.Error("an offline host cannot be attached to")
	}
}

func TestAReconnectReplacesTheOldSocket(t *testing.T) {
	h := newHubHarness(t, "jt")
	old, hostID := h.accepted("fp1")
	fresh, again := h.accepted("fp1")
	if again != hostID {
		t.Fatalf("the same fingerprint got two host ids: %s and %s", hostID, again)
	}

	// The corpse is dropped, and the live socket is the one that gets the traffic.
	if _, _, err := old.ws.Read(old.ctx); err == nil {
		t.Error("the replaced socket is still open")
	}
	if _, err := h.hub.OpenPTY(hostID, "s_1", wire.Size{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	if _, is := fresh.control().(*wire.PtyOpen); !is {
		t.Error("pty.open went to the wrong socket")
	}
	if !h.hub.Online(hostID) {
		t.Error("the host is online through its new socket")
	}
}

func read(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	got, err := io.ReadFull(r, buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:got])
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
