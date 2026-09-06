package worker

import (
	"context"
	"net"
	"sync"
)

const (
	// Bytes waiting to go into one proxied connection. Past this the tunnel's reader
	// blocks, which is what makes the stream an honest net.Conn end to end.
	portQueue = 32
	portRead  = 32 << 10
)

// portStream is one proxied TCP connection into a container: the control plane's bytes
// queue in `in` and feed the socket; the socket's bytes are pumped back by the tunnel.
type portStream struct {
	in     chan []byte
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once

	mu   sync.Mutex
	conn net.Conn
}

func newPortStream(cancel context.CancelFunc) *portStream {
	return &portStream{
		in:     make(chan []byte, portQueue),
		done:   make(chan struct{}),
		cancel: cancel,
	}
}

// attach sets the dialled socket, or reports that the stream was closed while dialling.
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

// feed drains the queue into the socket until either side is gone.
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
