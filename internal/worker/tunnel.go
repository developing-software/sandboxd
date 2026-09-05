package worker

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/wire"
)

// Dialer is what the tunnel needs of a driver: one TCP connection inside a container, for
// the preview proxy. The container half of the driver is declared next to the manager.
type Dialer interface {
	Dial(ctx context.Context, id string, port int) (net.Conn, error)
}

const (
	heartbeatInterval = 10 * time.Second
	minBackoff        = time.Second
	maxBackoff        = 30 * time.Second
	// One frame carries a whole PTY replay, so the limit is well above the ring.
	readLimit = 4 << 20
	// Frames waiting on the one writer goroutine. Port copiers and viewer pumps block
	// here; that is the backpressure, and it reaches the container's socket.
	writeQueue = 256
	// Bytes waiting to go into one proxied connection. Past this the tunnel's reader
	// blocks, which is what makes the stream an honest net.Conn end to end.
	portQueue = 32
	portRead  = 32 << 10
)

// Tunnel is the one outbound WebSocket to the control plane: JSON control messages as
// text frames, PTY bytes and proxied TCP as binary ones, all multiplexed by stream id.
type Tunnel struct {
	cfg    Config
	mgr    *Manager
	dialer Dialer
	events <-chan Event
	log    *slog.Logger

	mu  sync.Mutex
	cur *session
}

// session is everything that dies with one connection. A reconnect builds a new one:
// stream ids are only unique within a connection, so none of this survives.
type session struct {
	conn *websocket.Conn
	out  chan frame
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	streams map[uint32]*stream
}

type frame struct {
	typ  websocket.MessageType
	data []byte
	// ack, when set, is closed once this frame has reached the socket. Flush uses it to
	// know a shutdown's last messages actually went out.
	ack chan struct{}
}

// stream is one multiplexed channel: exactly one of the two halves is set. The sid lives
// here because the wire names a stream, not a sandbox — only the tunnel knows which is
// which.
type stream struct {
	sid    string
	viewer *Viewer
	port   *portStream
}

type portStream struct {
	in     chan []byte
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once

	mu   sync.Mutex
	conn net.Conn
}

func NewTunnel(cfg Config, mgr *Manager, dialer Dialer, events <-chan Event, log *slog.Logger) *Tunnel {
	return &Tunnel{cfg: cfg, mgr: mgr, dialer: dialer, events: events, log: log}
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

func (t *Tunnel) serve(ctx context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn.SetReadLimit(readLimit)
	s := &session{
		conn:    conn,
		out:     make(chan frame, writeQueue),
		done:    make(chan struct{}),
		streams: map[uint32]*stream{},
	}
	t.setCurrent(s)
	defer func() {
		t.setCurrent(nil)
		s.close()
		t.teardown(s)
		_ = conn.CloseNow()
	}()

	go t.write(ctx, s)

	running := t.mgr.Running()
	t.send(s, &wire.Hello{
		Name:         t.cfg.Name,
		Fingerprint:  t.cfg.Fingerprint,
		Running:      running,
		MaxSandboxes: t.cfg.MaxSandboxes,
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

func (t *Tunnel) pumpViewer(s *session, id uint32, v *Viewer) {
	for b := range v.Bytes() {
		t.sendBinary(s, id, b)
	}
	s.take(id)
	// A viewer we closed ourselves needs no announcement: the control plane asked.
	if !v.ClosedLocally() {
		t.send(s, &wire.PtyClosed{Stream: id})
	}
}

func (t *Tunnel) openPort(ctx context.Context, s *session, m *wire.PortDial) {
	ctx, cancel := context.WithCancel(ctx)
	ps := &portStream{
		in:     make(chan []byte, portQueue),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	// Registered before the dial, so a `port.close` that overtakes it still lands.
	s.add(m.Stream, &stream{sid: m.SID, port: ps})

	container, ok := t.mgr.ContainerOf(m.SID)
	if !ok {
		s.take(m.Stream)
		ps.close()
		t.send(s, &wire.PortError{Stream: m.Stream, Msg: "no such sandbox"})
		return
	}

	conn, err := t.dialer.Dial(ctx, container, m.Port)
	if err != nil {
		s.take(m.Stream)
		ps.close()
		if ctx.Err() == nil {
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

func (t *Tunnel) heartbeat(ctx context.Context, s *session) {
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			t.send(s, &wire.Heartbeat{Running: t.mgr.Count(), Max: t.cfg.MaxSandboxes})
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
			case Started:
				t.send(s, &wire.SandboxStarted{SID: ev.SID})
			case Ended:
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

func (t *Tunnel) teardown(s *session) {
	s.mu.Lock()
	streams := s.streams
	s.streams = map[uint32]*stream{}
	s.mu.Unlock()

	for _, st := range streams {
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

func (s *session) queue(f frame) {
	select {
	case s.out <- f:
	case <-s.done:
	}
}

func (s *session) add(id uint32, st *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams[id] = st
}

func (s *session) stream(id uint32) *stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

// take removes a stream and returns it, so exactly one caller ever cleans one up.
func (s *session) take(id uint32) *stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[id]
	delete(s.streams, id)
	return st
}

func (s *session) close() {
	s.once.Do(func() { close(s.done) })
}

func (p *portStream) attach(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return false
	default:
	}
	p.conn = conn
	return true
}

// write blocks the tunnel's reader once this stream's queue is full — the decided policy
// for proxied TCP, and the opposite of a PTY viewer's.
func (p *portStream) write(b []byte) {
	select {
	case p.in <- b:
	case <-p.done:
	}
}

func (p *portStream) feed() {
	for {
		select {
		case b := <-p.in:
			p.mu.Lock()
			conn := p.conn
			p.mu.Unlock()
			if conn == nil {
				return
			}
			if _, err := conn.Write(b); err != nil {
				return
			}
		case <-p.done:
			return
		}
	}
}

func (p *portStream) close() {
	p.once.Do(func() {
		close(p.done)
		p.cancel()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.conn != nil {
			_ = p.conn.Close()
		}
	})
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
