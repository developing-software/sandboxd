package worker

import "sync"

// The tail a reattaching browser replays. It lives here, on the host, for the whole life
// of the sandbox: the control plane stores no terminal bytes (DESIGN.md decision 4).
const DefaultRingCap = 256 << 10

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
