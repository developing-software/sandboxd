package worker

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/sandbox"
	"sandboxd/internal/wire"
)

const (
	heartbeatInterval = 10 * time.Second
	minBackoff        = time.Second
	maxBackoff        = 30 * time.Second
	// One frame carries a whole PTY replay, so the limit is well above the ring.
	readLimit = 4 << 20
)

// Tunnel is the one outbound WebSocket to the control plane: JSON control messages as
// text frames, PTY bytes and proxied TCP as binary ones, all multiplexed by stream id.
// The connection-scoped state is a `session`; a `portStream` is one proxied connection.
type Tunnel struct {
	cfg    Config
	mgr    *sandbox.Manager
	events <-chan sandbox.Event
	log    *slog.Logger

	mu  sync.Mutex
	cur *session
}

// NewTunnel is the remote face of a manager: what the manager reports, the tunnel says
// on the wire, and capacity is read from the manager rather than from the config.
func NewTunnel(cfg Config, mgr *sandbox.Manager, events <-chan sandbox.Event, log *slog.Logger) *Tunnel {
	return &Tunnel{cfg: cfg, mgr: mgr, events: events, log: log}
}

// Run dials the control plane and keeps dialling, with backoff, until the context ends.
func (t *Tunnel) Run(ctx context.Context) {
	go t.forwardEvents(ctx)

	backoff := minBackoff
	for ctx.Err() == nil {
		// The handshake response body is never the caller's to close: coder/websocket
		// documents that and nils it out on success (dial.go:110,147).
		conn, _, err := websocket.Dial(ctx, t.cfg.URL+"/tunnel", nil) //nolint:bodyclose
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			t.log.Warn("connect failed", "url", t.cfg.URL, "err", err, "retry_in", backoff)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		t.serve(ctx, conn)
	}
}

// Flush waits for what is already queued to reach the socket. Shutdown uses it so the
// `sandbox.ended` for every sandbox on this host is not lost with the connection.
func (t *Tunnel) Flush(ctx context.Context) {
	s := t.current()
	if s == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case s.out <- frame{ack: ack}:
	case <-s.done:
		return
	case <-ctx.Done():
		return
	}
	select {
	case <-ack:
	case <-s.done:
	case <-ctx.Done():
	}
}

// serve runs one connection: hello, then the read loop until the socket dies.
func (t *Tunnel) serve(ctx context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn.SetReadLimit(readLimit)
	s := newSession(conn)
	t.setCurrent(s)
	defer func() {
		t.setCurrent(nil)
		s.close()
		t.teardown(s)
		_ = conn.CloseNow()
	}()

	go t.write(ctx, s)

	_, max := t.mgr.Capacity()
	t.send(s, &wire.Hello{
		Name:         t.cfg.Name,
		Fingerprint:  t.cfg.Fingerprint,
		Running:      t.mgr.Running(),
		MaxSandboxes: max,
		Tags:         t.cfg.Tags,
		JoinToken:    t.cfg.JoinToken,
	})

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				t.log.Warn("disconnected", "err", err)
			}
			return
		}
		// coder/websocket hands out a fresh slice per message, so a payload may be
		// queued without copying it first.
		if typ == websocket.MessageText {
			t.control(ctx, s, data)
			continue
		}
		t.binary(s, data)
	}
}

// write is the session's only writer. Everything else queues.
func (t *Tunnel) write(ctx context.Context, s *session) {
	for {
		select {
		case f := <-s.out:
			if f.data != nil {
				if err := s.conn.Write(ctx, f.typ, f.data); err != nil {
					s.close()
					return
				}
			}
			if f.ack != nil {
				close(f.ack)
			}
		case <-s.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// --- inbound ---------------------------------------------------------------------------

func (t *Tunnel) control(ctx context.Context, s *session, raw []byte) {
	msg, err := wire.ParseFromCP(raw)
	if err != nil {
		t.log.Warn("unusable control message", "err", err)
		return
	}

	switch m := msg.(type) {
	case *wire.HelloOK:
		t.log.Info("connected", "host_id", m.HostID)
		go t.heartbeat(ctx, s)

	case *wire.HelloPending:
		// Printed, not logged: an operator reads this off the terminal and types it in.
		t.log.Info("waiting for approval on the control plane",
			"host_id", m.HostID, "code", m.Code,
			"hint", "or set SANDBOXD_JOIN_TOKEN here and on the control plane to skip this")

	case *wire.HelloRejected:
		t.log.Error("rejected by the control plane", "reason", m.Reason)

	case *wire.SandboxCreate:
		// Creating pulls an image and starts a container; the read loop keeps running.
		go t.mgr.Create(ctx, m.Spec)

	case *wire.SandboxDestroy:
		go t.mgr.End(ctx, m.SID, wire.EndClosed, "")

	case *wire.PtyOpen:
		t.openPTY(s, m)

	case *wire.PtyResize:
		t.mgr.Resize(m.SID, m.Size)

	case *wire.PtyClose:
		if st := s.take(m.Stream); st != nil && st.viewer != nil {
			st.viewer.Close()
		}

	case *wire.PortDial:
		go t.openPort(ctx, s, m)

	case *wire.PortClose:
		if st := s.take(m.Stream); st != nil && st.port != nil {
			st.port.close()
		}
	}
}

func (t *Tunnel) binary(s *session, data []byte) {
	id, payload, err := wire.Decode(data)
	if err != nil {
		t.log.Warn("unusable binary frame", "err", err)
		return
	}
	st := s.stream(id)
	if st == nil {
		return
	}
	if st.port != nil {
		st.port.write(payload)
		return
	}
	if st.viewer != nil {
		t.mgr.Write(st.sid, payload)
	}
}

// --- PTY streams -----------------------------------------------------------------------

func (t *Tunnel) openPTY(s *session, m *wire.PtyOpen) {
	v, replay, err := t.mgr.Attach(m.SID, m.Size)
	if err != nil {
		t.send(s, &wire.PtyClosed{Stream: m.Stream})
		return
	}
	s.add(m.Stream, &stream{sid: m.SID, viewer: v})

	// The marker first, then the tail, then live bytes — which are already queued in the
	// viewer's channel, so nothing can interleave.
	t.send(s, &wire.PtyReplay{Stream: m.Stream})
	if len(replay) > 0 {
		t.sendBinary(s, m.Stream, replay)
	}
	go t.pumpViewer(s, m.Stream, v)
}

func (t *Tunnel) pumpViewer(s *session, id uint32, v *sandbox.Viewer) {
	for b := range v.Bytes() {
		t.sendBinary(s, id, b)
	}
	s.take(id)
	// A viewer we closed ourselves needs no announcement: the control plane asked.
	if !v.ClosedLocally() {
		t.send(s, &wire.PtyClosed{Stream: id})
	}
}

// --- port streams ----------------------------------------------------------------------

func (t *Tunnel) openPort(ctx context.Context, s *session, m *wire.PortDial) {
	ctx, cancel := context.WithCancel(ctx)
	ps := newPortStream(cancel)
	// Registered before the dial, so a `port.close` that overtakes it still lands.
	s.add(m.Stream, &stream{sid: m.SID, port: ps})

	conn, err := t.mgr.Dial(ctx, m.SID, m.Port)
	if err != nil {
		s.take(m.Stream)
		// Read before close, which cancels the context: a dial the control plane gave up
		// on needs no answer, a failed one does.
		abandoned := ctx.Err() != nil
		ps.close()
		if !abandoned {
			t.send(s, &wire.PortError{Stream: m.Stream, Msg: err.Error()})
		}
		return
	}
	if !ps.attach(conn) { // closed while dialling
		_ = conn.Close()
		return
	}

	t.send(s, &wire.PortOpen{Stream: m.Stream})
	go ps.feed()
	go t.pumpPort(s, m.Stream, ps, conn)
}

// pumpPort copies the container's side into the tunnel. Blocking on the writer queue is
// the point: it stops reading the socket, and the backpressure reaches the app inside.
func (t *Tunnel) pumpPort(s *session, id uint32, ps *portStream, conn net.Conn) {
	buf := make([]byte, portRead)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			t.sendBinary(s, id, buf[:n])
		}
		if err != nil {
			break
		}
	}
	if s.take(id) != nil {
		t.send(s, &wire.PortClose{Stream: id})
	}
	ps.close()
}

// --- host → cp -------------------------------------------------------------------------

func (t *Tunnel) heartbeat(ctx context.Context, s *session) {
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			running, max := t.mgr.Capacity()
			t.send(s, &wire.Heartbeat{Running: running, Max: max})
		case <-s.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// forwardEvents turns what the manager did into what the control plane is told. A message
// raised while the tunnel is down is dropped: the next `hello` carries the truth.
func (t *Tunnel) forwardEvents(ctx context.Context) {
	for {
		select {
		case ev := <-t.events:
			s := t.current()
			if s == nil {
				continue
			}
			switch ev.Kind {
			case sandbox.Started:
				t.send(s, &wire.SandboxStarted{SID: ev.SID})
			case sandbox.Ended:
				t.send(s, &wire.SandboxEnded{SID: ev.SID, Reason: ev.Reason, Detail: ev.Detail})
			}
		case <-ctx.Done():
			return
		}
	}
}

func (t *Tunnel) send(s *session, m wire.FromHost) {
	raw, err := wire.Marshal(m)
	if err != nil {
		t.log.Error("marshal", "err", err)
		return
	}
	s.queue(frame{typ: websocket.MessageText, data: raw})
}

func (t *Tunnel) sendBinary(s *session, id uint32, payload []byte) {
	s.queue(frame{typ: websocket.MessageBinary, data: wire.Encode(id, payload)})
}

// teardown ends every stream because the connection is gone.
func (t *Tunnel) teardown(s *session) {
	for _, st := range s.takeAll() {
		if st.viewer != nil {
			st.viewer.Close()
		}
		if st.port != nil {
			st.port.close()
		}
	}
}

func (t *Tunnel) current() *session {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur
}

func (t *Tunnel) setCurrent(s *session) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur = s
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
