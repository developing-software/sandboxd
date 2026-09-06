package worker

import (
	"sync"

	"github.com/coder/websocket"

	"sandboxd/internal/sandbox"
)

// Frames waiting on the one writer goroutine. Port copiers and viewer pumps block here;
// that is the backpressure, and it reaches the container's socket.
const writeQueue = 256

// session is everything that dies with one connection to the control plane. A reconnect
// builds a new one: stream ids are only unique within a connection, so none of this
// survives. It is the worker's counterpart of the control plane's `hosts.Conn`.
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
	viewer *sandbox.Viewer
	port   *portStream
}

func newSession(conn *websocket.Conn) *session {
	return &session{
		conn:    conn,
		out:     make(chan frame, writeQueue),
		done:    make(chan struct{}),
		streams: map[uint32]*stream{},
	}
}

// queue hands a frame to the writer, or drops it once the session is over.
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

// takeAll empties the table for teardown; the caller ends what it gets back.
func (s *session) takeAll() map[uint32]*stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	streams := s.streams
	s.streams = map[uint32]*stream{}
	return streams
}

func (s *session) close() {
	s.once.Do(func() { close(s.done) })
}
