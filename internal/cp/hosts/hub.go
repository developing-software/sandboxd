package hosts

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// rejectGrace is how long a rejected worker's socket is kept alive so it can read why.
const rejectGrace = 2 * time.Second

// Hub is the registry of connected workers. It owns the tunnel sockets, runs enrollment
// on hello, and routes stream traffic to the right connection.
type Hub struct {
	store     enrollStore
	joinToken string
	// events carries what the scheduler reacts to. It is a constructor argument rather
	// than a subscription, so there is no hub-and-scheduler construction cycle to break
	// after the fact: neither knows the other's type.
	events chan<- cp.Event
	log    *slog.Logger

	mu    sync.Mutex
	conns map[string]*Conn
	// pending holds sockets that said hello and are waiting for an admin.
	pending map[string]*Conn
}

func NewHub(s enrollStore, joinToken string, events chan<- cp.Event, log *slog.Logger) *Hub {
	return &Hub{
		store:     s,
		joinToken: joinToken,
		events:    events,
		log:       log,
		conns:     map[string]*Conn{},
		pending:   map[string]*Conn{},
	}
}

// Serve handles GET /tunnel: one worker, for as long as its socket lasts.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request) {
	// No origin check: the peer is a daemon, not a browser, and it authenticates by
	// fingerprint in its hello rather than by where it was served from.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.log.Warn("tunnel upgrade failed", "from", r.RemoteAddr, "err", err)
		return
	}
	ws.SetReadLimit(readLimit)
	ctx := r.Context()

	c := newConn(ws, h.log)
	go c.writeLoop(ctx)
	defer h.finish(c)
	h.log.Info("worker connected", "from", r.RemoteAddr)

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			id, payload, err := wire.Decode(data)
			if err != nil {
				h.log.Warn("unusable binary frame", "host_id", c.HostID(), "err", err)
				continue
			}
			// The payload aliases the read buffer, and a WebSocket read allocates a fresh
			// one per message, so handing it to a stream passes ownership cleanly.
			c.onFrame(id, payload)
			continue
		}
		msg, err := wire.ParseFromHost(data)
		if err != nil {
			h.log.Warn("unusable control message", "host_id", c.HostID(), "err", err)
			continue
		}
		if err := h.onMessage(ctx, c, msg); err != nil {
			h.log.Error("host message", "host_id", c.HostID(), "err", err)
		}
	}
}

// finish tears one socket down: stop writing, end every stream, then forget the host.
func (h *Hub) finish(c *Conn) {
	c.shutdown()
	c.closeAll()
	hostID := c.HostID()
	if hostID == "" {
		h.log.Info("worker disconnected before hello")
		return
	}
	h.mu.Lock()
	live := h.conns[hostID] == c
	if live {
		delete(h.conns, hostID)
	}
	if h.pending[hostID] == c {
		delete(h.pending, hostID)
	}
	h.mu.Unlock()
	if live {
		h.log.Warn("host offline", "host_id", hostID)
	}
}

func (h *Hub) onMessage(ctx context.Context, c *Conn, msg wire.FromHost) error {
	if hello, ok := msg.(*wire.Hello); ok {
		return h.onHello(ctx, c, hello)
	}
	// Anything before or without approval is ignored: an unenrolled socket has no host to
	// act on behalf of.
	if !h.isLive(c) {
		return nil
	}
	switch m := msg.(type) {
	case *wire.Heartbeat:
		c.setCapacity(m.Running, m.Max)
		if err := h.store.TouchHost(c.HostID(), store.HostPatch{MaxSandboxes: &m.Max}); err != nil {
			return err
		}
		h.emit(ctx, cp.Heartbeat{HostID: c.HostID()})
	case *wire.SandboxStarted:
		h.emit(ctx, cp.SandboxStarted{SID: m.SID})
	case *wire.SandboxEnded:
		h.emit(ctx, cp.SandboxEnded{SID: m.SID, Reason: m.Reason, Detail: m.Detail})
	default:
		c.onStreamMsg(msg)
	}
	return nil
}

func (h *Hub) onHello(ctx context.Context, c *Conn, hello *wire.Hello) error {
	e, err := enroll(h.store, hello, h.joinToken)
	if err != nil {
		return fmt.Errorf("hosts: enroll %s: %w", hello.Name, err)
	}
	c.setHostID(e.host.ID)

	switch e.kind {
	case rejected:
		h.log.Warn("revoked host tried to connect", "host_id", e.host.ID, "name", e.host.Name)
		if err := c.send(&wire.HelloRejected{Reason: e.reason}); err != nil {
			return err
		}
		// Let the rejection reach the worker before the socket goes; otherwise it retries
		// in a loop with no idea why.
		c.flush(rejectGrace)
		c.shutdown()
	case pending:
		if e.badToken {
			h.log.Warn("join token did not match; waiting for the code",
				"host_id", e.host.ID, "name", e.host.Name)
		}
		h.log.Info("host waiting for approval",
			"host_id", e.host.ID, "name", e.host.Name, "code", e.host.ApproveCode, "new", e.isNew)
		h.mu.Lock()
		h.pending[e.host.ID] = c
		h.mu.Unlock()
		return c.send(&wire.HelloPending{HostID: e.host.ID, Code: e.host.ApproveCode})
	case accepted:
		if e.joined {
			h.log.Info("host approved by join token", "host_id", e.host.ID, "name", e.host.Name)
		}
		h.accept(ctx, c, e.host.ID, hello.Running, hello.MaxSandboxes)
	}
	return nil
}

// accept publishes the connection and tells the scheduler what the host is running, which
// is the input to reconciliation in both directions.
func (h *Hub) accept(ctx context.Context, c *Conn, hostID string, running []string, max int) {
	c.setCapacity(len(running), max)

	h.mu.Lock()
	old := h.conns[hostID]
	h.conns[hostID] = c
	delete(h.pending, hostID)
	h.mu.Unlock()

	// A second socket for the same host replaces the first: the worker reconnected and
	// the old one is a corpse we have not noticed yet.
	if old != nil && old != c {
		old.shutdown()
	}
	if err := c.send(&wire.HelloOK{HostID: hostID}); err != nil {
		return
	}
	h.log.Info("host online", "host_id", hostID, "running", len(running), "max", max)
	h.emit(ctx, cp.HostOnline{HostID: hostID, Running: running})
}

// NotifyApproved promotes a socket that is already waiting, so an approved worker does
// not have to reconnect to find out.
func (h *Hub) NotifyApproved(hostID string) {
	h.mu.Lock()
	c := h.pending[hostID]
	h.mu.Unlock()
	if c == nil {
		return
	}
	host, found, err := h.store.Host(hostID)
	if err != nil || !found {
		return
	}
	h.accept(context.Background(), c, hostID, nil, host.MaxSandboxes)
}

func (h *Hub) emit(ctx context.Context, e cp.Event) {
	select {
	case h.events <- e:
	case <-ctx.Done():
	}
}

func (h *Hub) isLive(c *Conn) bool {
	hostID := c.HostID()
	if hostID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[hostID] == c
}

func (h *Hub) conn(hostID string) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[hostID]
}

// --- what the rest of the control plane calls -------------------------------------------

func (h *Hub) Online(hostID string) bool { return h.conn(hostID) != nil }

func (h *Hub) Capacity(hostID string) (cp.Capacity, bool) {
	c := h.conn(hostID)
	if c == nil {
		return cp.Capacity{}, false
	}
	return c.Capacity(), true
}

// CreateSandbox reports whether the host took the order. The slot is counted at once,
// optimistically, so a burst of placements cannot oversubscribe a host before its next
// heartbeat corrects the number.
func (h *Hub) CreateSandbox(hostID string, spec wire.Spec) bool {
	c := h.conn(hostID)
	if c == nil {
		return false
	}
	if err := c.send(&wire.SandboxCreate{Spec: spec}); err != nil {
		return false
	}
	got := c.Capacity()
	c.setCapacity(got.Running+1, got.Max)
	return true
}

func (h *Hub) DestroySandbox(hostID, sid string) {
	if c := h.conn(hostID); c != nil {
		_ = c.send(&wire.SandboxDestroy{SID: sid})
	}
}

// OpenPTY attaches a viewer to a sandbox's terminal. Spelled out rather than returned
// straight through, for the same reason as Dial: a nil *Stream in an interface is not nil.
func (h *Hub) OpenPTY(hostID, sid string, size wire.Size) (cp.PTY, error) {
	c := h.conn(hostID)
	if c == nil {
		return nil, errHostGone
	}
	s, err := c.openPTY(sid, size)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Dial opens a TCP connection to a port inside a sandbox. The returned net.Conn is what
// the preview proxy's transport hands to net/http.
func (h *Hub) Dial(ctx context.Context, hostID, sid string, port int) (net.Conn, error) {
	c := h.conn(hostID)
	if c == nil {
		return nil, errHostGone
	}
	// Spelled out rather than returned straight through: a nil *Stream in a net.Conn is a
	// non-nil interface, and every caller of a dialler tests the connection for nil.
	s, err := c.dial(ctx, sid, port)
	if err != nil {
		return nil, err
	}
	return s, nil
}
