package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"sandboxd/internal/wire"
)

// EventKind is what happened to a sandbox. The manager's owner turns each into what the
// control plane hears: the tunnel into a wire message, a provider into a scheduler event.
type EventKind int

const (
	Started EventKind = iota
	Ended
)

type Event struct {
	Kind   EventKind
	SID    string
	Reason wire.EndReason
	Detail string
}

var ErrNoSandbox = errors.New("sandbox: no such sandbox")

const (
	reapInterval = 15 * time.Second
	ptyReadSize  = 32 << 10
	inputQueue   = 64
)

// The size a PTY starts at, before the first viewer says otherwise.
var initialSize = wire.Size{Cols: 120, Rows: 40}

// Manager owns every sandbox running on this host: its container, its PTY, its ring
// buffer and its idle timer, all torn down together.
type Manager struct {
	ctx    context.Context
	drv    Driver
	entry  []string
	max    int
	events chan<- Event
	log    *slog.Logger

	mu    sync.Mutex
	boxes map[string]*sandbox
}

type sandbox struct {
	spec      wire.Spec
	idle      time.Duration // fixed at create, so the reaper needs no lock for it
	container string
	pty       PTY
	fan       *Fanout
	in        chan []byte
	done      chan struct{}

	mu     sync.Mutex
	size   wire.Size
	ready  bool
	ending bool
}

// NewManager takes the daemon's context because a sandbox outlives any one request: the
// PTY pump, the input feed and the idle reaper all end with it, and so does the delivery
// of an event to an owner that has stopped reading. entry is the command exec'd when a
// spec names none; max is the capacity this host reports, held here because how many
// sandboxes a runtime can hold is a fact about the runtime, not about what surrounds it.
func NewManager(
	ctx context.Context, drv Driver, entry []string, max int, events chan<- Event, log *slog.Logger,
) *Manager {
	return &Manager{
		ctx:    ctx,
		drv:    drv,
		entry:  entry,
		max:    max,
		events: events,
		log:    log,
		boxes:  map[string]*sandbox{},
	}
}

// Run reaps idle sandboxes until the context ends, then ends every one as lost.
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(reapInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			m.reapIdle(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// Create runs one sandbox: a container kept idle, then the command exec'd in a PTY inside
// it. Failure at any step ends the sandbox rather than leaving a half-built one.
func (m *Manager) Create(ctx context.Context, spec wire.Spec) {
	// Reserve the id before the slow calls, so two `sandbox.create` for one sid cannot
	// both build a container.
	m.mu.Lock()
	if _, taken := m.boxes[spec.SID]; taken {
		m.mu.Unlock()
		return
	}
	box := &sandbox{
		spec: spec,
		idle: time.Duration(spec.IdleTimeoutS) * time.Second,
		fan:  NewFanout(DefaultRingCap),
		in:   make(chan []byte, inputQueue),
		done: make(chan struct{}),
		size: initialSize,
	}
	m.boxes[spec.SID] = box
	m.mu.Unlock()

	if err := m.start(ctx, box); err != nil {
		m.log.Error("sandbox create failed", "sid", spec.SID, "err", err)
		m.forget(spec.SID)
		m.emit(Event{Kind: Ended, SID: spec.SID, Reason: wire.EndFailed, Detail: err.Error()})
		return
	}

	m.log.Info("sandbox started", "sid", spec.SID, "container", short(box.container))
	m.emit(Event{Kind: Started, SID: spec.SID})
}

func (m *Manager) start(ctx context.Context, box *sandbox) error {
	spec := box.spec
	id, err := m.drv.Create(ctx, spec.SID, spec.Image)
	if err != nil {
		return err
	}
	box.container = id

	cmd := spec.Cmd
	if len(cmd) == 0 {
		cmd = m.entry
	}
	pty, err := m.drv.Attach(ctx, id, cmd, sandboxEnv(spec), box.size)
	if err != nil {
		// The container exists and nothing will ever attach to it again.
		if rmErr := m.drv.Destroy(ctx, id); rmErr != nil {
			m.log.Warn("destroy after failed attach", "sid", spec.SID, "err", rmErr)
		}
		return err
	}

	box.mu.Lock()
	box.pty = pty
	box.ready = true
	// The driver has the secrets now, and this copy is the last one in the worker.
	box.spec.SecretEnv = nil
	box.mu.Unlock()

	go m.pump(box)
	go m.feed(box)
	return nil
}

// sandboxEnv is the container's environment, in precedence order: the sandbox's own env,
// then its secrets, then the two names the sandbox contract reserves.
func sandboxEnv(spec wire.Spec) map[string]string {
	env := make(map[string]string, len(spec.Env)+len(spec.SecretEnv)+2)
	maps.Copy(env, spec.Env)
	maps.Copy(env, spec.SecretEnv)
	env["TERM"] = "xterm-256color"
	// Kept from the session era on purpose: every preset's entry.sh reads this name.
	env["SANDBOXD_SESSION_ID"] = spec.SID
	return env
}

// pump is the PTY's output: into the ring, out to every viewer, until the process exits.
func (m *Manager) pump(box *sandbox) {
	buf := make([]byte, ptyReadSize)
	for {
		n, err := box.pty.Read(buf)
		if n > 0 {
			box.fan.Emit(slices.Clone(buf[:n]))
		}
		if err != nil {
			m.End(m.ctx, box.spec.SID, wire.EndExited, "")
			return
		}
	}
}

// feed is the PTY's input. It is a goroutine so that a container which has stopped reading
// stalls only its own sandbox, never the tunnel that carries every other one.
func (m *Manager) feed(box *sandbox) {
	for {
		select {
		case b := <-box.in:
			if _, err := box.pty.Write(b); err != nil {
				return
			}
		case <-box.done:
			return
		}
	}
}

// Attach subscribes a viewer and returns the tail to replay before its bytes.
func (m *Manager) Attach(sid string, size wire.Size) (*Viewer, []byte, error) {
	box, err := m.ready(sid)
	if err != nil {
		return nil, nil, err
	}
	v, replay := box.fan.Subscribe()
	m.Resize(sid, size)
	return v, replay, nil
}

// Write is one viewer's keystrokes. It blocks only this sandbox, and only until its own
// queue drains.
func (m *Manager) Write(sid string, b []byte) {
	box, err := m.ready(sid)
	if err != nil {
		return
	}
	box.fan.Touch()
	select {
	case box.in <- b:
	case <-box.done:
	}
}

// Resize is last-writer-wins: several viewers share one terminal, and the last one to
// speak owns its size.
func (m *Manager) Resize(sid string, size wire.Size) {
	box, err := m.ready(sid)
	if err != nil {
		return
	}
	box.mu.Lock()
	unchanged := box.size == size
	box.size = size
	pty := box.pty
	box.mu.Unlock()

	if unchanged {
		return
	}
	if err := pty.Resize(m.ctx, size); err != nil {
		m.log.Warn("pty resize", "sid", sid, "err", err)
	}
}

// End tears a sandbox down once: the PTY, its viewers, its input, then the container.
func (m *Manager) End(ctx context.Context, sid string, reason wire.EndReason, detail string) {
	m.mu.Lock()
	box, ok := m.boxes[sid]
	if !ok {
		m.mu.Unlock()
		return
	}
	box.mu.Lock()
	ending := box.ending
	box.ending = true
	pty := box.pty
	box.mu.Unlock()
	if ending {
		m.mu.Unlock()
		return
	}
	delete(m.boxes, sid)
	m.mu.Unlock()

	if pty != nil {
		_ = pty.Close()
	}
	// Viewers learn before the control plane does, so a `pty.closed` never arrives after
	// the sandbox it belonged to is gone.
	box.fan.Close()
	close(box.done)

	if box.container != "" {
		if err := m.drv.Destroy(ctx, box.container); err != nil {
			m.log.Warn("destroy", "sid", sid, "container", short(box.container), "err", err)
		}
	}
	m.log.Info("sandbox ended", "sid", sid, "reason", reason)
	m.emit(Event{Kind: Ended, SID: sid, Reason: reason, Detail: detail})
}

// EndAll is shutdown: every sandbox on this host is reported lost, because the PTY and its
// ring die with this process and cannot be re-attached (a v1 limitation).
func (m *Manager) EndAll(ctx context.Context, reason wire.EndReason) {
	for _, sid := range m.Running() {
		m.End(ctx, sid, reason, "")
	}
}

// Sweep removes containers left by a previous run of this worker identity. Their PTYs and
// ring buffers died with that process, so there is nothing to re-attach to.
func (m *Manager) Sweep(ctx context.Context) error {
	managed, err := m.drv.ListManaged(ctx)
	if err != nil {
		return fmt.Errorf("sandbox: sweep: %w", err)
	}
	for _, c := range managed {
		m.log.Warn("removing orphaned container from a previous run", "sid", c.SID, "container", short(c.ID))
		if err := m.drv.Destroy(ctx, c.ID); err != nil {
			m.log.Warn("sweep destroy", "sid", c.SID, "err", err)
		}
	}
	return nil
}

func (m *Manager) Running() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.boxes))
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.boxes)
}

// Capacity is what the host reports upward. The worker's hello and heartbeat and an
// embedded provider's answer to the scheduler all read it from here.
func (m *Manager) Capacity() (running, max int) {
	return m.Count(), m.max
}

// Dial opens a TCP connection to a port inside a sandbox, for the preview proxy. Only a
// running sandbox has a container to dial into.
func (m *Manager) Dial(ctx context.Context, sid string, port int) (net.Conn, error) {
	box, err := m.ready(sid)
	if err != nil {
		return nil, err
	}
	return m.drv.Dial(ctx, box.container, port)
}

func (m *Manager) reapIdle(ctx context.Context) {
	m.mu.Lock()
	boxes := maps.Clone(m.boxes)
	m.mu.Unlock()

	now := time.Now()
	for sid, box := range boxes {
		if box.idle > 0 && now.Sub(box.fan.LastActivity()) > box.idle {
			m.End(ctx, sid, wire.EndIdle, "")
		}
	}
}

// ready returns a sandbox that has a PTY. A reserved-but-not-yet-built one is not a
// sandbox anybody can write to.
func (m *Manager) ready(sid string) (*sandbox, error) {
	m.mu.Lock()
	box, ok := m.boxes[sid]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoSandbox, sid)
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	if !box.ready {
		return nil, fmt.Errorf("%w: %s is still starting", ErrNoSandbox, sid)
	}
	return box, nil
}

func (m *Manager) forget(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.boxes, sid)
}

// emit never blocks past the daemon's life: a tunnel that has stopped reading is a tunnel
// that is going away, and the control plane reconciles from the next `hello` anyway.
func (m *Manager) emit(ev Event) {
	select {
	case m.events <- ev:
	case <-m.ctx.Done():
	}
}

func short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
