package sandbox

import (
	"sync"
	"time"
)

// How far a viewer may fall behind before it is dropped instead of buffered. A dropped
// viewer costs the browser one reconnect and replays the ring; an undropped one would cost
// the worker unbounded memory.
const viewerQueue = 256

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
