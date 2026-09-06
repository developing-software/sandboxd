// Package sandboxtest is the fake driver every manager owner tests against. The driver
// is the boundary to Docker, not one of our own modules, so a fake is the right tool here
// — unlike the store on the control-plane side, which stays real.
package sandboxtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"testing"

	"sandboxd/internal/sandbox"
	"sandboxd/internal/wire"
)

// Driver records every call and hands out a PTY per container. FailPull names an image
// whose create fails; FailDial makes every dial refuse; Managed is what the sweep finds.
type Driver struct {
	FailPull string
	FailDial bool
	Managed  []sandbox.Managed

	mu    sync.Mutex
	calls []string
	ptys  map[string]*PTY
	n     int
}

func New() *Driver {
	return &Driver{ptys: map[string]*PTY{}}
}

func (d *Driver) Create(_ context.Context, sid, img string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if img == d.FailPull {
		return "", fmt.Errorf("pull failed: %s", img)
	}
	d.n++
	id := fmt.Sprintf("c%d", d.n)
	d.calls = append(d.calls, "create "+id)
	return id, nil
}

func (d *Driver) Attach(
	_ context.Context, id string, cmd []string, env map[string]string, size wire.Size,
) (sandbox.PTY, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	d.calls = append(d.calls, fmt.Sprintf("attach %s %v %v %dx%d", id, cmd, keys, size.Cols, size.Rows))

	pty := newPTY()
	d.ptys[id] = pty
	return pty, nil
}

// Dial answers with an echo server, as if something inside the sandbox were listening.
func (d *Driver) Dial(_ context.Context, id string, port int) (net.Conn, error) {
	d.mu.Lock()
	d.calls = append(d.calls, fmt.Sprintf("dial %s:%d", id, port))
	fail := d.FailDial
	d.mu.Unlock()
	if fail {
		return nil, errors.New("connection refused")
	}
	ours, theirs := net.Pipe()
	go func() { _, _ = io.Copy(theirs, theirs) }()
	return ours, nil
}

func (d *Driver) Destroy(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "destroy "+id)
	return nil
}

func (d *Driver) ListManaged(context.Context) ([]sandbox.Managed, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.Managed), nil
}

// Calls is every driver call so far, in order.
func (d *Driver) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

func (d *Driver) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = nil
}

// PTY returns the terminal attached to a container, failing the test if there is none.
func (d *Driver) PTY(t *testing.T, id string) *PTY {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	pty, ok := d.ptys[id]
	if !ok {
		t.Fatalf("no PTY for container %s", id)
	}
	return pty
}

// PTY is a process in a terminal: what the test says comes out of Read, what the sandbox
// writes lands in Input.
type PTY struct {
	Input   chan []byte
	Resizes chan wire.Size

	out  *io.PipeReader
	outW *io.PipeWriter
	once sync.Once
}

func newPTY() *PTY {
	r, w := io.Pipe()
	return &PTY{out: r, outW: w, Input: make(chan []byte, 16), Resizes: make(chan wire.Size, 16)}
}

func (p *PTY) Read(b []byte) (int, error) { return p.out.Read(b) }

func (p *PTY) Write(b []byte) (int, error) {
	p.Input <- slices.Clone(b)
	return len(b), nil
}

func (p *PTY) Close() error {
	p.once.Do(func() {
		_ = p.outW.Close()
		_ = p.out.Close()
	})
	return nil
}

func (p *PTY) Resize(_ context.Context, size wire.Size) error {
	select {
	case p.Resizes <- size:
	default:
	}
	return nil
}

// Say is the process printing something. It returns once the manager has read it.
func (p *PTY) Say(t *testing.T, s string) {
	t.Helper()
	if _, err := p.outW.Write([]byte(s)); err != nil {
		t.Fatalf("write to fake PTY: %v", err)
	}
}

// Exit is the process returning, which the manager's pump sees as EOF.
func (p *PTY) Exit() { _ = p.outW.Close() }
