package hosts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"sandboxd/internal/wire"
)

// Stream is one multiplexed stream on a host's tunnel, as a net.Conn.
//
// This is the type the whole preview proxy rests on: `httputil.ReverseProxy` dials one of
// these and then does HTTP, chunked bodies and WebSocket upgrades with no protocol code
// of ours. The PTY side is the same type with pty.* messages and a Resize.
type Stream struct {
	conn *Conn
	id   uint32
	sid  string
	port int
	// pty streams drop when the reader is slow and close with pty.close; port streams
	// block and close with port.close.
	pty bool

	// in carries payloads from the host. Only the connection's read goroutine sends on
	// it, and only that goroutine closes it, so a remote close cannot race a send.
	in   chan []byte
	rest []byte

	// open resolves a port.dial: closed on port.open, and on port.error with dialErr set.
	open    chan struct{}
	dialErr error

	once sync.Once
	done chan struct{}
	// ended is set before the inbound channel closes, so a reader that has seen EOF can
	// rely on Close having nothing left to tell the host.
	ended atomic.Bool

	rd, wd deadline
}

// The queue in front of a reader, and the policy when it fills. A PTY viewer is dropped
// (the worker's 256 KB ring replays the tail when the browser reattaches), a port stream
// blocks (that is the TCP backpressure the net.Conn promises, and it is why the queue is
// per stream: one hung sandbox must not stall the whole tunnel).
const (
	ptyQueue  = 256
	portQueue = 32
)

func newStream(c *Conn, id uint32, sid string, port int, pty bool) *Stream {
	size := portQueue
	if pty {
		size = ptyQueue
	}
	return &Stream{
		conn: c, id: id, sid: sid, port: port, pty: pty,
		in:   make(chan []byte, size),
		open: make(chan struct{}),
		done: make(chan struct{}),
	}
}

func (s *Stream) Read(b []byte) (int, error) {
	if len(s.rest) > 0 {
		n := copy(b, s.rest)
		s.rest = s.rest[n:]
		return n, nil
	}
	// What has already arrived comes first: a stream that ended still owes its reader the
	// bytes it buffered, and a plain select would race the close against them.
	select {
	case p, ok := <-s.in:
		return s.take(b, p, ok)
	default:
	}
	select {
	case p, ok := <-s.in:
		return s.take(b, p, ok)
	case <-s.done:
		return 0, net.ErrClosed
	case <-s.rd.wait():
		return 0, os.ErrDeadlineExceeded
	}
}

func (s *Stream) take(b, payload []byte, ok bool) (int, error) {
	if !ok {
		return 0, io.EOF
	}
	n := copy(b, payload)
	s.rest = payload[n:]
	return n, nil
}

func (s *Stream) Write(b []byte) (int, error) {
	if err := s.conn.sendFrame(s.id, b, s.done, s.wd.wait()); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close ends the stream from this side and tells the host so. Closing twice is a no-op,
// which matters because ReverseProxy and its caller both close.
func (s *Stream) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.conn.forget(s.id)
		// Nothing to say when the host is the one that hung up.
		if s.ended.Load() {
			return
		}
		var msg wire.FromCP = &wire.PortClose{Stream: s.id}
		if s.pty {
			msg = &wire.PtyClose{Stream: s.id}
		}
		// Best effort: a dead socket has already ended every stream on it.
		_ = s.conn.send(msg)
	})
	return nil
}

// Resize retargets the terminal. PTY streams only; the message carries the sandbox id
// rather than the stream, because every viewer of a sandbox shares one terminal.
func (s *Stream) Resize(size wire.Size) error {
	if !s.pty {
		return errors.New("hosts: resize on a port stream")
	}
	return s.conn.send(&wire.PtyResize{SID: s.sid, Size: size})
}

func (s *Stream) LocalAddr() net.Addr { return addr{"sandboxd", "cp"} }

func (s *Stream) RemoteAddr() net.Addr {
	if s.pty {
		return addr{"sandboxd", s.sid + "/pty"}
	}
	return addr{"sandboxd", fmt.Sprintf("%s:%d", s.sid, s.port)}
}

func (s *Stream) SetDeadline(t time.Time) error {
	s.rd.set(t)
	s.wd.set(t)
	return nil
}

func (s *Stream) SetReadDeadline(t time.Time) error  { s.rd.set(t); return nil }
func (s *Stream) SetWriteDeadline(t time.Time) error { s.wd.set(t); return nil }

// --- what the connection's read goroutine calls ---------------------------------------

// deliver hands over a payload. Ownership passes to the stream: the caller must not reuse
// the slice, which holds because a WebSocket read allocates its own buffer per message.
func (s *Stream) deliver(p []byte) {
	if s.pty {
		select {
		case s.in <- p:
		default: // dropped, per the policy above
		}
		return
	}
	select {
	case s.in <- p:
	case <-s.done:
	}
}

// finish ends the stream because the host said so, or because the socket died. Buffered
// payloads still drain: closing `in` is what becomes io.EOF once the reader catches up.
func (s *Stream) finish() {
	// Flagged before the channel closes: a reader that reaches EOF has then already
	// observed that a local Close has nothing left to send.
	s.ended.Store(true)
	close(s.in)
	s.once.Do(func() { close(s.done) })
}

// opened resolves a pending dial. err is nil for port.open, set for port.error.
func (s *Stream) opened(err error) {
	s.dialErr = err
	close(s.open)
}

// awaitOpen blocks until the host answers a port.dial, the stream or the socket dies, or
// the caller gives up.
func (s *Stream) awaitOpen(ctx context.Context, host <-chan struct{}) error {
	select {
	case <-s.open:
		return s.dialErr
	case <-s.done:
		return net.ErrClosed
	case <-host:
		return errHostGone
	case <-ctx.Done():
		return ctx.Err()
	}
}

type addr struct{ network, s string }

func (a addr) Network() string { return a.network }
func (a addr) String() string  { return a.s }

// deadline is the net.Conn deadline contract: a channel closed once the time has passed,
// so a blocked Read or Write can select on it. The shape is the standard library's
// net.Pipe deadline — the timer is stopped and awaited under the lock, so a firing
// callback cannot close a channel a later SetDeadline has already replaced.
type deadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel chan struct{}
}

func (d *deadline) wait() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel == nil {
		d.cancel = make(chan struct{})
	}
	return d.cancel
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // the callback is already running; let it finish closing
	}
	d.timer = nil

	if d.cancel == nil || closed(d.cancel) {
		d.cancel = make(chan struct{})
	}
	if t.IsZero() {
		return
	}
	if until := time.Until(t); until > 0 {
		d.timer = time.AfterFunc(until, func() { close(d.cancel) })
		return
	}
	close(d.cancel)
}

func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
