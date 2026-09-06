package worker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/sandbox"
	"sandboxd/internal/sandbox/sandboxtest"
	"sandboxd/internal/wire"
)

// fakeCP is the control plane's half of the tunnel: a real WebSocket server speaking the
// real wire. The transport is the boundary here, so this is the one fake the worker suite
// is allowed.
type fakeCP struct {
	srv   *httptest.Server
	conns chan *cpConn
}

type cpConn struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func newFakeCP(t *testing.T) *fakeCP {
	t.Helper()
	cp := &fakeCP{conns: make(chan *cpConn, 4)}
	cp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tunnel" {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		conn.SetReadLimit(readLimit)
		cp.conns <- &cpConn{t: t, conn: conn, ctx: r.Context()}
		<-r.Context().Done()
	}))
	t.Cleanup(cp.srv.Close)
	return cp
}

func (cp *fakeCP) accept(t *testing.T) *cpConn {
	t.Helper()
	select {
	case c := <-cp.conns:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no worker connected")
		return nil
	}
}

// next reads the next control message, skipping binary frames the caller did not ask for.
func (c *cpConn) next(t *testing.T) wire.FromHost {
	t.Helper()
	for {
		typ, data, err := c.read(t)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		msg, err := wire.ParseFromHost(data)
		if err != nil {
			t.Fatalf("parse %s: %v", data, err)
		}
		return msg
	}
}

// binary reads the next binary frame on a stream, skipping control messages.
func (c *cpConn) binary(t *testing.T, want uint32) []byte {
	t.Helper()
	for {
		typ, data, err := c.read(t)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		id, payload, err := wire.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if id == want {
			return payload
		}
	}
}

func (c *cpConn) read(t *testing.T) (websocket.MessageType, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	return c.conn.Read(ctx)
}

func (c *cpConn) send(t *testing.T, m wire.FromCP) {
	t.Helper()
	raw, err := wire.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *cpConn) sendBinary(t *testing.T, id uint32, payload []byte) {
	t.Helper()
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, wire.Encode(id, payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

type tunnelHarness struct {
	ctx    context.Context
	mgr    *sandbox.Manager
	drv    *sandboxtest.Driver
	cp     *fakeCP
	tunnel *Tunnel
	cfg    Config
}

func newTunnelHarness(t *testing.T) *tunnelHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cp := newFakeCP(t)
	cfg := Config{
		URL:         cp.srv.URL,
		Name:        "box",
		Fingerprint: "fp-1234567890",
		Tags:        []string{"arch:" + runtime.GOARCH, "driver:docker", "os:" + runtime.GOOS},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	drv := sandboxtest.New()
	events := make(chan sandbox.Event, 32)
	mgr := sandbox.NewManager(ctx, drv, []string{"/entry"}, 4, events, log)
	tunnel := NewTunnel(cfg, mgr, events, log)
	go tunnel.Run(ctx)
	return &tunnelHarness{ctx: ctx, mgr: mgr, drv: drv, cp: cp, tunnel: tunnel, cfg: cfg}
}

// The whole sequence, in one test, because the sequence is what phase 2 has to prove:
// hello, create, attach, replay, input, resize, a proxied port, then destroy.
func TestTunnelCarriesASandboxEndToEnd(t *testing.T) {
	h := newTunnelHarness(t)
	cp := h.cp.accept(t)

	hello, ok := h.cp.hello(t, cp)
	if !ok {
		t.Fatalf("first message = %T, want hello", hello)
	}
	if hello.Name != "box" || hello.Fingerprint != "fp-1234567890" || hello.MaxSandboxes != 4 {
		t.Errorf("hello = %+v", hello)
	}
	if !reflect.DeepEqual(hello.Tags, h.cfg.Tags) {
		t.Errorf("hello.tags = %v, want %v", hello.Tags, h.cfg.Tags)
	}
	if len(hello.Running) != 0 {
		t.Errorf("hello.running = %v, want nothing running yet", hello.Running)
	}
	cp.send(t, &wire.HelloOK{HostID: "h_1"})

	// --- create -----------------------------------------------------------------
	cp.send(t, &wire.SandboxCreate{Spec: wire.Spec{
		SID: "s_1", Image: "sandbox:1", IdleTimeoutS: 60,
		Env: map[string]string{"A": "1"}, SecretEnv: map[string]string{"S": "x"},
	}})
	if msg := cp.next(t); !isStarted(msg, "s_1") {
		t.Fatalf("after create: %#v, want sandbox.started s_1", msg)
	}
	pty := h.drv.PTY(t, "c1")

	// --- attach, and the replay marker before the tail --------------------------
	// A throwaway viewer proves the bytes reached the ring: the fanout pushes there and
	// to every viewer under one lock, so once this one has them, so does the ring.
	witness, _, err := h.mgr.Attach("s_1", wire.Size{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	pty.Say(t, "printed before attaching")
	<-witness.Bytes()
	witness.Close()

	cp.send(t, &wire.PtyOpen{SID: "s_1", Stream: 1, Size: wire.Size{Cols: 100, Rows: 30}})
	replay, isReplay := cp.next(t).(*wire.PtyReplay)
	if !isReplay || replay.Stream != 1 {
		t.Fatalf("after pty.open: %#v, want pty.replay on stream 1", replay)
	}
	if got := string(cp.binary(t, 1)); got != "printed before attaching" {
		t.Errorf("replayed %q", got)
	}
	if got := <-pty.Resizes; got != (wire.Size{Cols: 100, Rows: 30}) {
		t.Errorf("resize on attach = %+v", got)
	}

	// --- live output, then input ------------------------------------------------
	pty.Say(t, "live")
	if got := string(cp.binary(t, 1)); got != "live" {
		t.Errorf("live output = %q", got)
	}

	cp.sendBinary(t, 1, []byte("typed"))
	if got := string(<-pty.Input); got != "typed" {
		t.Errorf("input = %q", got)
	}

	cp.send(t, &wire.PtyResize{SID: "s_1", Size: wire.Size{Cols: 80, Rows: 24}})
	if got := <-pty.Resizes; got != (wire.Size{Cols: 80, Rows: 24}) {
		t.Errorf("resize = %+v", got)
	}

	// --- a proxied port ---------------------------------------------------------
	cp.send(t, &wire.PortDial{SID: "s_1", Port: 8080, Stream: 3})
	if msg := cp.next(t); !isPortOpen(msg, 3) {
		t.Fatalf("after port.dial: %#v, want port.open on stream 3", msg)
	}
	cp.sendBinary(t, 3, []byte("GET / HTTP/1.1\r\n"))
	if got := string(cp.binary(t, 3)); got != "GET / HTTP/1.1\r\n" {
		t.Errorf("the sandbox echoed %q", got)
	}

	// --- destroy ----------------------------------------------------------------
	cp.send(t, &wire.SandboxDestroy{SID: "s_1"})
	ended := waitForEnded(t, cp)
	if ended.SID != "s_1" || ended.Reason != wire.EndClosed {
		t.Errorf("ended = %+v, want s_1 closed", ended)
	}
	if h.mgr.Count() != 0 {
		t.Error("the sandbox should be gone")
	}
}

// A sandbox the control plane never heard of has no container to dial.
func TestPortDialForAnUnknownSandboxIsAnError(t *testing.T) {
	h := newTunnelHarness(t)
	cp := h.cp.accept(t)
	h.cp.hello(t, cp)
	cp.send(t, &wire.HelloOK{HostID: "h_1"})

	cp.send(t, &wire.PortDial{SID: "s_nope", Port: 8080, Stream: 5})
	msg := cp.next(t)
	fail, ok := msg.(*wire.PortError)
	if !ok || fail.Stream != 5 {
		t.Fatalf("got %#v, want port.error on stream 5", msg)
	}
}

// Attaching to a sandbox that is not there closes the stream rather than hanging it.
func TestPtyOpenForAnUnknownSandboxClosesTheStream(t *testing.T) {
	h := newTunnelHarness(t)
	cp := h.cp.accept(t)
	h.cp.hello(t, cp)
	cp.send(t, &wire.HelloOK{HostID: "h_1"})

	cp.send(t, &wire.PtyOpen{SID: "s_nope", Stream: 7, Size: wire.Size{Cols: 80, Rows: 24}})
	msg := cp.next(t)
	closed, ok := msg.(*wire.PtyClosed)
	if !ok || closed.Stream != 7 {
		t.Fatalf("got %#v, want pty.closed on stream 7", msg)
	}
}

// The control plane restarting must not take the host with it, and the second hello
// reports what is actually running — which is how the CP reconciles.
func TestTunnelReconnectsAndReportsWhatIsRunning(t *testing.T) {
	h := newTunnelHarness(t)
	first := h.cp.accept(t)
	h.cp.hello(t, first)
	first.send(t, &wire.HelloOK{HostID: "h_1"})

	first.send(t, &wire.SandboxCreate{Spec: wire.Spec{SID: "s_1", Image: "sandbox:1", IdleTimeoutS: 60}})
	if msg := first.next(t); !isStarted(msg, "s_1") {
		t.Fatalf("%#v, want sandbox.started", msg)
	}

	_ = first.conn.Close(websocket.StatusGoingAway, "restarting")

	second := h.cp.accept(t)
	hello, _ := h.cp.hello(t, second)
	if !reflect.DeepEqual(hello.Running, []string{"s_1"}) {
		t.Errorf("hello.running = %v, want the sandbox that survived the reconnect", hello.Running)
	}
}

func (cp *fakeCP) hello(t *testing.T, c *cpConn) (*wire.Hello, bool) {
	t.Helper()
	msg := c.next(t)
	hello, ok := msg.(*wire.Hello)
	if !ok {
		t.Fatalf("first message = %#v, want hello", msg)
	}
	return hello, ok
}

func isStarted(m wire.FromHost, sid string) bool {
	started, ok := m.(*wire.SandboxStarted)
	return ok && started.SID == sid
}

func isPortOpen(m wire.FromHost, stream uint32) bool {
	open, ok := m.(*wire.PortOpen)
	return ok && open.Stream == stream
}

// The PTY streams are closed before the sandbox is reported ended, so pty.closed may
// arrive first; either order is the sandbox going away.
func waitForEnded(t *testing.T, c *cpConn) *wire.SandboxEnded {
	t.Helper()
	for range 5 {
		if ended, ok := c.next(t).(*wire.SandboxEnded); ok {
			return ended
		}
	}
	t.Fatal("no sandbox.ended")
	return nil
}
