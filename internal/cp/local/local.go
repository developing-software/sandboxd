// Package local runs sandboxes inside the control plane process: one sandbox.Manager over
// any sandbox.Driver, presented to the fleet as one host. It knows nothing about Docker
// — cmd/sandboxd-api builds the driver from `providers.docker` and hands it in, exactly
// as cmd/sandboxd-worker does from `driver.docker`.
//
// The trade, written down: a sandbox here does not survive a control plane restart. Its
// PTY was an exec held by this process; at boot the provider reports nothing running, the
// row reconciles to `lost`, and the orphan sweep removes the container. The worker path
// keeps that property; choosing this gives it up for one fewer daemon.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/sandbox"
	"sandboxd/internal/wire"
)

// ProviderTag is what every host here carries, beside the driver's own facts.
const ProviderTag = "provider:local"

// heartbeatInterval matches the worker's, so the scheduler drains the queue and refreshes
// last_seen_at at the same cadence whichever provider a host is behind.
const heartbeatInterval = 10 * time.Second

// Options is the provider's own configuration plus what the manager needs from the
// driver's block. Driver is the driver's name: it fixes the fingerprint.
type Options struct {
	Name   string
	Tags   []string
	Driver string
	Entry  []string
	Max    int
}

// What the provider needs from the store: its own row, and the heartbeat's touch.
type hostStore interface {
	UpsertProviderHost(h store.Host) (store.Host, error)
	TouchHost(id string, p store.HostPatch) error
}

type Provider struct {
	ctx    context.Context
	id     string
	max    int
	mgr    *sandbox.Manager
	store  hostStore
	events chan<- cp.Event
	inbox  chan sandbox.Event
	log    *slog.Logger

	// running is counted optimistically at CreateSandbox and resynced from the manager
	// on every event and heartbeat, exactly as the hub does for a worker: a burst of
	// placements must not oversubscribe the host before the manager has reserved.
	mu      sync.Mutex
	running int
}

// Fingerprint is a provider host's identity: one per driver per control plane, stable
// across restarts so the row — and the sandboxes placed on it — keep their ids.
func Fingerprint(driver string) string {
	sum := sha256.Sum256([]byte("sandboxd/provider/" + driver))
	return hex.EncodeToString(sum[:])
}

// New upserts the host row, sweeps what a previous run left, and builds the manager. ctx
// is the daemon's: sandboxes here outlive any request and end with the process.
func New(
	ctx context.Context, drv sandbox.Driver, o Options, st hostStore, events chan<- cp.Event, log *slog.Logger,
) (*Provider, error) {
	tags := slices.Clone(o.Tags)
	if !slices.Contains(tags, ProviderTag) {
		tags = slices.Sorted(slices.Values(append(tags, ProviderTag)))
	}
	host, err := st.UpsertProviderHost(store.Host{
		Name: o.Name, Fingerprint: Fingerprint(o.Driver), MaxSandboxes: o.Max, Tags: tags,
	})
	if err != nil {
		return nil, err
	}
	log = log.With("host_id", host.ID, "provider", o.Driver)
	inbox := make(chan sandbox.Event, 64)
	p := &Provider{
		ctx:    ctx,
		id:     host.ID,
		max:    o.Max,
		mgr:    sandbox.NewManager(ctx, drv, o.Entry, o.Max, inbox, log),
		store:  st,
		events: events,
		inbox:  inbox,
		log:    log,
	}
	// A sandbox from a previous run cannot be re-attached: its PTY and ring died with
	// that process, so the container goes — before anything is placed here.
	if err := p.mgr.Sweep(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// ID is the host id the row was given.
func (p *Provider) ID() string { return p.id }

// Run reports like a worker would: online with nothing running, then a heartbeat on the
// interval and each sandbox as it starts and ends, so reconciliation covers a restart
// with no new path. It returns when ctx ends.
func (p *Provider) Run(ctx context.Context) {
	p.emit(ctx, cp.HostOnline{HostID: p.id, Running: []string{}})
	go p.mgr.Run(ctx)

	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case ev := <-p.inbox:
			p.resync()
			switch ev.Kind {
			case sandbox.Started:
				p.emit(ctx, cp.SandboxStarted{SID: ev.SID})
			case sandbox.Ended:
				p.emit(ctx, cp.SandboxEnded{SID: ev.SID, Reason: ev.Reason, Detail: ev.Detail})
			}
		case <-tick.C:
			p.resync()
			if err := p.store.TouchHost(p.id, store.HostPatch{}); err != nil {
				p.log.Error("heartbeat", "err", err)
			}
			p.emit(ctx, cp.Heartbeat{HostID: p.id})
		case <-ctx.Done():
			return
		}
	}
}

// Close ends every sandbox as lost, for shutdown: the PTYs die with this process.
func (p *Provider) Close(ctx context.Context) { p.mgr.EndAll(ctx, wire.EndLost) }

func (p *Provider) emit(ctx context.Context, e cp.Event) {
	select {
	case p.events <- e:
	case <-ctx.Done():
	}
}

func (p *Provider) resync() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running, _ = p.mgr.Capacity()
}

// --- what the fleet calls --------------------------------------------------------------

func (p *Provider) Online(hostID string) bool { return hostID == p.id }

func (p *Provider) Capacity(hostID string) (cp.Capacity, bool) {
	if hostID != p.id {
		return cp.Capacity{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return cp.Capacity{Running: p.running, Max: p.max}, true
}

// CreateSandbox takes the order and builds in the background: creating pulls an image,
// and the caller is the scheduler goroutine.
func (p *Provider) CreateSandbox(hostID string, spec wire.Spec) bool {
	if hostID != p.id {
		return false
	}
	p.mu.Lock()
	p.running++
	p.mu.Unlock()
	go p.mgr.Create(p.ctx, spec)
	return true
}

func (p *Provider) DestroySandbox(hostID, sid string) {
	if hostID == p.id {
		go p.mgr.End(p.ctx, sid, wire.EndClosed, "")
	}
}

// NotifyApproved is nothing here: a provider host is approved at boot and never waits.
func (p *Provider) NotifyApproved(string) {}

// OpenPTY returns a terminal whose Read yields the ring's tail and then live bytes. The
// full-queue policy is the manager's: a viewer that falls behind is dropped and reads EOF.
func (p *Provider) OpenPTY(hostID, sid string, size wire.Size) (cp.PTY, error) {
	if hostID != p.id {
		return nil, sandbox.ErrNoSandbox
	}
	v, replay, err := p.mgr.Attach(sid, size)
	if err != nil {
		return nil, err
	}
	return &pty{mgr: p.mgr, sid: sid, viewer: v, rest: replay}, nil
}

func (p *Provider) Dial(ctx context.Context, hostID, sid string, port int) (net.Conn, error) {
	if hostID != p.id {
		return nil, sandbox.ErrNoSandbox
	}
	return p.mgr.Dial(ctx, sid, port)
}

// pty is one viewer as the attach bridge sees it.
type pty struct {
	mgr    *sandbox.Manager
	sid    string
	viewer *sandbox.Viewer
	rest   []byte
}

func (t *pty) Read(b []byte) (int, error) {
	if len(t.rest) == 0 {
		chunk, ok := <-t.viewer.Bytes()
		if !ok {
			return 0, io.EOF
		}
		t.rest = chunk
	}
	n := copy(b, t.rest)
	t.rest = t.rest[n:]
	return n, nil
}

// Write copies: the manager keeps what it is handed until the PTY takes it, and the
// bridge reuses its buffer.
func (t *pty) Write(b []byte) (int, error) {
	t.mgr.Write(t.sid, slices.Clone(b))
	return len(b), nil
}

func (t *pty) Resize(size wire.Size) error {
	t.mgr.Resize(t.sid, size)
	return nil
}

func (t *pty) Close() error {
	t.viewer.Close()
	return nil
}
