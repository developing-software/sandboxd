package worker

import (
	"sync"
	"time"
)

// The tail a reattaching browser replays. It lives here, on the host, for the whole life
// of the sandbox: the control plane stores no terminal bytes (DESIGN.md decision 4).
const DefaultRingCap = 256 << 10

// How far a viewer may fall behind before it is dropped instead of buffered. A dropped
// viewer costs the browser one reconnect and replays the ring; an undropped one costs the
// worker unbounded memory, which is what today's TypeScript does.
const viewerQueue = 256

// Ring keeps the last cap bytes of one PTY's output.
type Ring struct {
	mu     sync.Mutex
	chunks [][]byte
	size   int
	cap    int
}

func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultRingCap
	}
	return &Ring{cap: capacity}
}

// Push takes ownership of b.
func (r *Ring) Push(b []byte) {
	if len(b) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.chunks = append(r.chunks, b)
	r.size += len(b)
	for r.size > r.cap && len(r.chunks) > 1 {
		r.size -= len(r.chunks[0])
		r.chunks = r.chunks[1:]
	}
	// One chunk larger than the whole ring: keep its tail, not the chunk.
	if len(r.chunks) == 1 && len(r.chunks[0]) > r.cap {
		r.chunks[0] = r.chunks[0][len(r.chunks[0])-r.cap:]
		r.size = r.cap
	}
}

func (r *Ring) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]byte, 0, r.size)
	for _, c := range r.chunks {
		out = append(out, c...)
	}
	return out
}

// Fanout is one sandbox's output: the ring, the viewers watching it, and the clock the
// idle reaper reads.
type Fanout struct {
	mu     sync.Mutex
	ring   *Ring
	subs   map[*Viewer]struct{}
	last   time.Time
	closed bool
}

func NewFanout(capacity int) *Fanout {
	return &Fanout{ring: NewRing(capacity), subs: map[*Viewer]struct{}{}, last: time.Now()}
}

// Emit takes ownership of b: it is buffered and handed to every viewer without copying.
func (f *Fanout) Emit(b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.last = time.Now()
	f.ring.Push(b)
	for v := range f.subs {
		select {
		case v.ch <- b:
		default:
			// This viewer is too far behind to be worth memory. The ring owns catch-up.
			f.drop(v)
		}
	}
}

// Touch counts a viewer's keystrokes as activity, which is what keeps a terminal someone
// is typing into from being reaped as idle (DESIGN.md decision 10).
func (f *Fanout) Touch() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = time.Now()
}

func (f *Fanout) LastActivity() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// Subscribe returns a viewer and the bytes to replay before its channel is read.
func (f *Fanout) Subscribe() (*Viewer, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v := &Viewer{ch: make(chan []byte, viewerQueue), fan: f}
	if f.closed {
		close(v.ch)
		return v, nil
	}
	f.subs[v] = struct{}{}
	return v, f.ring.Snapshot()
}

// Close ends every viewer: the sandbox is gone, and each viewer's stream is told so.
func (f *Fanout) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true
	for v := range f.subs {
		f.drop(v)
	}
}

// drop runs under f.mu, which is why it never calls back into the viewer.
func (f *Fanout) drop(v *Viewer) {
	delete(f.subs, v)
	close(v.ch)
}

func (f *Fanout) viewers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// Viewer is one attached terminal. Its channel closes when the viewer goes away — either
// because it fell behind, or because the sandbox ended, or because its own stream was
// closed from the control plane. Only the last of those is silent on the wire.
type Viewer struct {
	ch    chan []byte
	fan   *Fanout
	local bool
	mu    sync.Mutex
}

func (v *Viewer) Bytes() <-chan []byte { return v.ch }

// Close unsubscribes on this side, so the reader learns the stream ended without a
// `pty.closed` going back to a control plane that already knows.
func (v *Viewer) Close() {
	v.mu.Lock()
	if v.local {
		v.mu.Unlock()
		return
	}
	v.local = true
	v.mu.Unlock()

	v.fan.mu.Lock()
	defer v.fan.mu.Unlock()
	if _, ok := v.fan.subs[v]; ok {
		v.fan.drop(v)
	}
}

// ClosedLocally distinguishes "we hung up" from "we were dropped or the sandbox ended".
func (v *Viewer) ClosedLocally() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.local
}
