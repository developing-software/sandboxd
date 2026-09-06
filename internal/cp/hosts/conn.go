// Package hosts owns the worker tunnels: one WebSocket per host, multiplexed into
// streams, plus enrollment and the host admin use cases. Everything above it — the
// scheduler, the attach bridge, the preview proxy — sees either a narrow interface or a
// net.Conn, never the socket.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/cp"
	"sandboxd/internal/wire"
)

const (
	// writeQueue is the buffer in front of the one writer goroutine.
	writeQueue = 256
	// writeTimeout bounds a single frame. A socket that cannot take a frame in this long
	// is wedged, and dropping it beats stalling every stream behind it.
	writeTimeout = 10 * time.Second
	// controlWait bounds how long a control message waits for room on the queue. See
	// send: the caller is usually the scheduler goroutine, and it may not wait longer.
	controlWait = time.Second
	readLimit   = 4 << 20
)

var errHostGone = errors.New("hosts: host is offline")

type outgoing struct {
	typ  websocket.MessageType
	data []byte
	// ack, when set, is closed once this entry has left the queue. An entry with no data
	// is an ack and nothing else, which is how flush waits for the socket to catch up.
	ack chan struct{}
}

// Conn is one connected host. It knows nothing about enrollment or the store: the hub
// decides who this is, and the conn multiplexes.
type Conn struct {
	ws  *websocket.Conn
	log *slog.Logger

	// hostID is the host this socket enrolled as. It is written once, on the read
	// goroutine, but writeLoop is already running by then and names the host when a write
	// fails, so it is atomic rather than plain.
	hostID atomic.Pointer[string]

	out       chan outgoing
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	running int
	max     int
	// next is the id of the stream to allocate. CP-allocated ids are odd and are never
	// reused within a connection (DESIGN.md, "Wire protocol").
	next    uint32
	streams map[uint32]*Stream
}

func newConn(ws *websocket.Conn, log *slog.Logger) *Conn {
	return &Conn{
		ws:      ws,
		log:     log,
		out:     make(chan outgoing, writeQueue),
		done:    make(chan struct{}),
		next:    1,
		streams: map[uint32]*Stream{},
	}
}

// HostID is the host this socket enrolled as, empty until it has said hello.
func (c *Conn) HostID() string {
	if id := c.hostID.Load(); id != nil {
		return *id
	}
	return ""
}

// setHostID names the socket, once, when enrollment has decided who it is.
func (c *Conn) setHostID(id string) { c.hostID.Store(&id) }

// Capacity is what the host last reported.
func (c *Conn) Capacity() cp.Capacity {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cp.Capacity{Running: c.running, Max: c.max}
}

func (c *Conn) setCapacity(running, max int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running, c.max = running, max
}

// --- the one writer -------------------------------------------------------------------

// writeLoop is the only thing that calls Write on the socket. It ends when the queue's
// owner shuts the connection down, when the server stops, or on the first write error.
func (c *Conn) writeLoop(ctx context.Context) {
	defer c.shutdown()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case m := <-c.out:
			if m.data != nil {
				wctx, cancel := context.WithTimeout(ctx, writeTimeout)
				err := c.ws.Write(wctx, m.typ, m.data)
				cancel()
				if err != nil {
					c.log.Warn("tunnel write failed", "host_id", c.HostID(), "err", err)
					return
				}
			}
			if m.ack != nil {
				close(m.ack)
			}
		}
	}
}

// flush waits for everything already queued to reach the socket, so a message sent just
// before the connection is dropped — hello.rejected — is not lost to the close.
func (c *Conn) flush(timeout time.Duration) {
	ack := make(chan struct{})
	select {
	case c.out <- outgoing{ack: ack}:
	case <-c.done:
		return
	}
	select {
	case <-ack:
	case <-c.done:
	case <-time.After(timeout):
	}
}

// send queues a control message. Control is never dropped silently: a lost sandbox.create
// would leave a row in `creating` with nothing behind it, so a caller that cannot queue
// one is told the host is gone and treats it as an unplaced sandbox.
//
// The policy when the queue is full, which this stream kind owes the same way a PTY and a
// port stream do: wait controlWait, then drop the tunnel. The caller here is usually the
// scheduler goroutine — the only writer of sandbox state — and the queue it is waiting on
// carries every PTY and port frame for this host too. Blocking until writeTimeout retired
// the socket would stall every API call in the process behind one wedged worker. A host
// that cannot take a control frame in a second is gone; dropping it makes the worker
// reconnect, and hello reconciles both directions.
func (c *Conn) send(m wire.FromCP) error {
	b, err := wire.Marshal(m)
	if err != nil {
		return err
	}
	timer := time.NewTimer(controlWait)
	defer timer.Stop()
	select {
	case c.out <- outgoing{typ: websocket.MessageText, data: b}:
		return nil
	case <-c.done:
		return errHostGone
	case <-timer.C:
		c.log.Warn("control queue stalled; dropping the tunnel",
			"host_id", c.HostID(), "after", controlWait)
		c.shutdown()
		return errHostGone
	}
}

// sendFrame queues stream bytes, honouring the stream's own lifetime and write deadline.
func (c *Conn) sendFrame(id uint32, payload []byte, done, deadline <-chan struct{}) error {
	select {
	case <-done:
		return net.ErrClosed
	default:
	}
	select {
	case c.out <- outgoing{typ: websocket.MessageBinary, data: wire.Encode(id, payload)}:
		return nil
	case <-done:
		return net.ErrClosed
	case <-c.done:
		return errHostGone
	case <-deadline:
		return os.ErrDeadlineExceeded
	}
}

// shutdown ends the socket. Streams are ended by the read goroutine's closeAll, which is
// the only place allowed to close a stream's inbound channel.
func (c *Conn) shutdown() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.CloseNow()
	})
}

// --- streams ---------------------------------------------------------------------------

func (c *Conn) alloc(sid string, port int, pty bool) (*Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return nil, errHostGone
	default:
	}
	s := newStream(c, c.next, sid, port, pty)
	c.next += 2
	c.streams[s.id] = s
	return s, nil
}

func (c *Conn) forget(id uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.streams, id)
}

func (c *Conn) stream(id uint32) *Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[id]
}

// openPTY attaches a viewer to a sandbox's terminal. There is no acknowledgement to wait
// for: the host answers with pty.replay and then bytes.
func (c *Conn) openPTY(sid string, size wire.Size) (*Stream, error) {
	s, err := c.alloc(sid, 0, true)
	if err != nil {
		return nil, err
	}
	if err := c.send(&wire.PtyOpen{SID: sid, Stream: s.id, Size: size}); err != nil {
		c.forget(s.id)
		return nil, err
	}
	return s, nil
}

// dial opens a TCP connection inside a sandbox and waits for the host to say whether it
// worked, so the caller gets a connection or an error and never a half-open stream.
func (c *Conn) dial(ctx context.Context, sid string, port int) (*Stream, error) {
	s, err := c.alloc(sid, port, false)
	if err != nil {
		return nil, err
	}
	if err := c.send(&wire.PortDial{SID: sid, Port: port, Stream: s.id}); err != nil {
		c.forget(s.id)
		return nil, err
	}
	if err := s.awaitOpen(ctx, c.done); err != nil {
		// Close is a no-op when the host already ended the stream with port.error.
		_ = s.Close()
		return nil, fmt.Errorf("hosts: dial %s:%d: %w", sid, port, err)
	}
	return s, nil
}

// endStream is the host hanging up: pty.closed, port.close, or port.error.
func (c *Conn) endStream(id uint32) {
	c.mu.Lock()
	s := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if s != nil {
		s.finish()
	}
}

// closeAll ends every stream because the socket is gone. Only the read goroutine calls it.
func (c *Conn) closeAll() {
	c.mu.Lock()
	streams := make([]*Stream, 0, len(c.streams))
	for _, s := range c.streams {
		streams = append(streams, s)
	}
	clear(c.streams)
	c.mu.Unlock()
	for _, s := range streams {
		s.finish()
	}
}

// --- inbound ---------------------------------------------------------------------------

func (c *Conn) onFrame(id uint32, payload []byte) {
	if s := c.stream(id); s != nil {
		s.deliver(payload)
	}
}

// onStreamMsg handles the host messages that address one stream rather than the host.
func (c *Conn) onStreamMsg(m wire.FromHost) {
	switch m := m.(type) {
	case *wire.PtyReplay:
		// A marker for the viewer, and the CP forwards bytes verbatim: the browser sees
		// the tail and then live output, which is what "replay" means to it.
	case *wire.PortOpen:
		if s := c.stream(m.Stream); s != nil {
			s.opened(nil)
		}
	case *wire.PortError:
		if s := c.stream(m.Stream); s != nil {
			s.opened(errors.New(m.Msg))
		}
		c.endStream(m.Stream)
	case *wire.PtyClosed:
		c.endStream(m.Stream)
	case *wire.PortClose:
		c.endStream(m.Stream)
	default:
		c.log.Warn("unroutable host message", "host_id", c.HostID(), "type", fmt.Sprintf("%T", m))
	}
}
