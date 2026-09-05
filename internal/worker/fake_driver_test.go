package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"testing"

	"sandboxd/internal/wire"
	"sandboxd/internal/worker/driver"
)

// The driver is the boundary to Docker, not one of our own modules, so a fake is the
// right tool here — unlike the store on the control-plane side, which stays real.
type fakeDriver struct {
	mu       sync.Mutex
	calls    []string
	created  []string
	ptys     map[string]*fakePTY
	managed  []driver.Managed
	failPull string
	failDial bool
	n        int
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{ptys: map[string]*fakePTY{}}
}

func (d *fakeDriver) Create(_ context.Context, sid, img string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if img == d.failPull {
		return "", fmt.Errorf("pull failed: %s", img)
	}
	d.n++
	id := fmt.Sprintf("c%d", d.n)
	d.calls = append(d.calls, "create "+id)
	d.created = append(d.created, sid)
	return id, nil
}

func (d *fakeDriver) Attach(
	_ context.Context, id string, cmd []string, env map[string]string, size wire.Size,
) (driver.PTY, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	d.calls = append(d.calls, fmt.Sprintf("attach %s %v %v %dx%d", id, cmd, keys, size.Cols, size.Rows))

	pty := newFakePTY()
	d.ptys[id] = pty
	return pty, nil
}

func (d *fakeDriver) Dial(_ context.Context, id string, port int) (net.Conn, error) {
	d.mu.Lock()
	d.calls = append(d.calls, fmt.Sprintf("dial %s:%d", id, port))
	fail := d.failDial
	d.mu.Unlock()
	if fail {
		return nil, errors.New("connection refused")
	}
	ours, theirs := net.Pipe()
	go func() { _, _ = io.Copy(theirs, theirs) }() // an echo server inside the sandbox
	return ours, nil
}

func (d *fakeDriver) Destroy(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "destroy "+id)
	return nil
}

func (d *fakeDriver) ListManaged(context.Context) ([]driver.Managed, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.managed), nil
}

func (d *fakeDriver) log() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

func (d *fakeDriver) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = nil
}

func (d *fakeDriver) pty(t *testing.T, id string) *fakePTY {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	pty, ok := d.ptys[id]
	if !ok {
		t.Fatalf("no PTY for container %s", id)
	}
	return pty
}

// fakePTY is a process in a terminal: what the test writes comes out of Read, what the
// sandbox writes lands in input.
type fakePTY struct {
	out     *io.PipeReader
	outW    *io.PipeWriter
	input   chan []byte
	resizes chan wire.Size
	once    sync.Once
}

func newFakePTY() *fakePTY {
	r, w := io.Pipe()
	return &fakePTY{out: r, outW: w, input: make(chan []byte, 16), resizes: make(chan wire.Size, 16)}
}

func (p *fakePTY) Read(b []byte) (int, error) { return p.out.Read(b) }

func (p *fakePTY) Write(b []byte) (int, error) {
	p.input <- slices.Clone(b)
	return len(b), nil
}

func (p *fakePTY) Close() error {
	p.once.Do(func() {
		_ = p.outW.Close()
		_ = p.out.Close()
	})
	return nil
}

func (p *fakePTY) Resize(_ context.Context, size wire.Size) error {
	select {
	case p.resizes <- size:
	default:
	}
	return nil
}

// say is the process printing something.
func (p *fakePTY) say(t *testing.T, s string) {
	t.Helper()
	if _, err := p.outW.Write([]byte(s)); err != nil {
		t.Fatalf("write to fake PTY: %v", err)
	}
}

// exit is the process returning, which the pump sees as EOF.
func (p *fakePTY) exit() { _ = p.outW.Close() }
