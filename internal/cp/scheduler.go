package cp

import (
	"context"
	"log/slog"
	"maps"
	"time"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// Placement, the FIFO queue and reconciliation. Secrets for queued sandboxes live only
// here, in memory, and never reach a row.
//
// The scheduler is one goroutine over two channels rather than a struct under a mutex. It
// is the only writer of sandbox state, so serialising it removes the whole "no lock held
// while sending" question from the largest piece of logic in the control plane — and the
// secrets map below needs no lock at all.

const (
	// unknownGrace is how long a sandbox may stay unaccounted for after a CP restart
	// before it is written off as lost.
	unknownGrace = 90 * time.Second
	unknownSweep = 15 * time.Second
)

// Event is what reaches the scheduler's goroutine. The hub sends the four below; an API
// call arrives on the same channel as a `call`, which is what makes the order a caller
// observes the order the loop applies.
type Event interface{ event() }

type (
	// HostOnline carries the sids the host says it is running, which is the input to
	// reconciliation in both directions.
	HostOnline struct {
		HostID  string
		Running []string
	}
	Heartbeat      struct{ HostID string }
	SandboxStarted struct{ SID string }
	SandboxEnded   struct {
		SID    string
		Reason wire.EndReason
		Detail string
	}
)

// call is an API request waiting on the loop. It shares the channel with the host events
// so the two cannot be reordered against each other.
type call struct{ run func() }

func (HostOnline) event()     {}
func (Heartbeat) event()      {}
func (SandboxStarted) event() {}
func (SandboxEnded) event()   {}
func (call) event()           {}

// What the scheduler needs from the store, and from the hub. Both are use-case shaped:
// there is no query builder here, and there never will be while one control plane and a
// handful of hosts is the whole design (DESIGN.md).
type (
	schedStore interface {
		Hosts() ([]store.Host, error)
		InsertSandbox(sb store.Sandbox) error
		Sandbox(id string) (store.Sandbox, bool, error)
		Queued() ([]store.Sandbox, error)
		Active() ([]store.Sandbox, error)
		ActiveOnHost(hostID string) ([]store.Sandbox, error)
		MarkCreating(id, hostID string) error
		MarkRunning(id string) error
		MarkKnown(id string) error
		MarkEnded(id string, reason wire.EndReason, detail string) error
		MarkActiveUnknown() error
	}
	placement interface {
		Online(hostID string) bool
		Capacity(hostID string) (Capacity, bool)
		CreateSandbox(hostID string, spec wire.Spec) bool
		DestroySandbox(hostID, sid string)
	}
)

type Scheduler struct {
	store      schedStore
	hub        placement
	sandboxEnv map[string]string
	policy     Policy
	log        *slog.Logger

	events chan Event

	// secrets holds `secret_env` per queued sandbox until placement. Only Run's goroutine
	// touches it, which is the point of the actor loop.
	secrets map[string]map[string]string
}

// NewScheduler wires the loop. sandboxEnv is operator-level env merged under every
// sandbox at placement time and never persisted; policy ranks the hosts that could take a
// sandbox (policy.go), and MostFreeSlots is the one v1 ships.
func NewScheduler(
	s schedStore, hub placement, policy Policy, sandboxEnv map[string]string,
	events chan Event, log *slog.Logger,
) *Scheduler {
	return &Scheduler{
		store: s, hub: hub, policy: policy, sandboxEnv: sandboxEnv, log: log,
		events:  events,
		secrets: map[string]map[string]string{},
	}
}

// Boot reconciles what a restart cost us, before Run starts. Active sandboxes are doubted
// until their host re-reports them; queued ones lost the secrets they were waiting on and
// cannot be placed, so they end rather than run without them.
func (s *Scheduler) Boot() error {
	if err := s.store.MarkActiveUnknown(); err != nil {
		return err
	}
	queued, err := s.store.Queued()
	if err != nil {
		return err
	}
	for _, sb := range queued {
		err := s.store.MarkEnded(sb.ID, wire.EndFailed,
			"control plane restarted before placement; secrets are not persisted")
		if err != nil {
			return err
		}
	}
	return nil
}

// Run is the loop. It owns every write to sandbox state and returns when ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(unknownSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.events:
			s.handle(e)
		case <-ticker.C:
			s.reapUnknown()
		}
	}
}

// Submit writes a new sandbox and places it if a host is free. It returns once the
// scheduler has decided, so the caller can read the row back and see `creating`.
//
// The row is inserted here rather than by the caller, and that is load-bearing: a queued
// row is visible to the next drain the moment it exists, and a drain that got there first
// would place the sandbox a second time and without the secrets it is waiting on. Doing
// both on this goroutine is what makes the row and its secrets appear together.
func (s *Scheduler) Submit(ctx context.Context, sb store.Sandbox, secretEnv map[string]string) error {
	return s.do(ctx, func() error {
		s.secrets[sb.ID] = secretEnv
		if err := s.store.InsertSandbox(sb); err != nil {
			delete(s.secrets, sb.ID)
			return err
		}
		placed, err := s.place(sb)
		if err != nil {
			return err
		}
		if !placed {
			s.log.Info("queued", "sid", sb.ID, "tags", sb.Tags)
		}
		return nil
	})
}

// Cancel ends a sandbox at the caller's request. A queued one ends here; a placed one is
// told to stop and ends when its host says so, unless the host is not there to tell.
//
// It takes an id rather than a row for the same reason Submit does the insert: the row a
// handler read may have been placed by a drain since, and cancelling from that copy would
// end the row while leaving the container running.
func (s *Scheduler) Cancel(ctx context.Context, sid string) error {
	return s.do(ctx, func() error {
		delete(s.secrets, sid)
		sb, found, err := s.store.Sandbox(sid)
		if err != nil {
			return err
		}
		if !found || sb.Status == store.Ended {
			return nil
		}
		if sb.Status == store.Queued {
			return s.store.MarkEnded(sid, wire.EndClosed, "")
		}
		if sb.HostID != nil {
			s.hub.DestroySandbox(*sb.HostID, sid)
		}
		if sb.HostID == nil || !s.hub.Online(*sb.HostID) {
			return s.store.MarkEnded(sid, wire.EndClosed, "host offline at close")
		}
		return nil
	})
}

// do runs f on the scheduler's goroutine and waits for it.
func (s *Scheduler) do(ctx context.Context, f func() error) error {
	reply := make(chan error, 1)
	select {
	case s.events <- call{run: func() { reply <- f() }}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- on the loop's goroutine -------------------------------------------------------------

func (s *Scheduler) handle(e Event) {
	var err error
	switch e := e.(type) {
	case call:
		e.run()
		return
	case HostOnline:
		err = s.hostOnline(e.HostID, e.Running)
	case Heartbeat:
		err = s.drain()
	case SandboxStarted:
		err = s.store.MarkRunning(e.SID)
	case SandboxEnded:
		delete(s.secrets, e.SID)
		if err = s.store.MarkEnded(e.SID, e.Reason, e.Detail); err == nil {
			err = s.drain()
		}
	}
	if err != nil {
		s.log.Error("scheduler event", "event", e, "err", err)
	}
}

// hostOnline reconciles in both directions: rows the host no longer runs are ended, and
// containers the store has no row for are reclaimed. The second direction exists because
// placement sends sandbox.create before it marks the row, so a crash in that window
// leaves a container nobody owns.
func (s *Scheduler) hostOnline(hostID string, running []string) error {
	reported := make(map[string]struct{}, len(running))
	for _, sid := range running {
		reported[sid] = struct{}{}
	}
	active, err := s.store.ActiveOnHost(hostID)
	if err != nil {
		return err
	}
	ours := make(map[string]struct{}, len(active))
	for _, sb := range active {
		ours[sb.ID] = struct{}{}
		if _, ok := reported[sb.ID]; ok {
			err = s.store.MarkKnown(sb.ID)
		} else {
			err = s.store.MarkEnded(sb.ID, wire.EndLost, "host reconnected without this sandbox")
		}
		if err != nil {
			return err
		}
	}
	for _, sid := range running {
		if _, ok := ours[sid]; !ok {
			s.log.Warn("host reports a sandbox we do not own; reclaiming",
				"host_id", hostID, "sid", sid)
			s.hub.DestroySandbox(hostID, sid)
		}
	}
	return s.drain()
}

// drain walks the queue oldest first. A sandbox that cannot be placed is skipped rather
// than blocking the queue behind it: with tags the queue is no longer homogeneous, and
// one unsatisfiable-for-now requirement must not starve everything after it.
func (s *Scheduler) drain() error {
	queued, err := s.store.Queued()
	if err != nil {
		return err
	}
	for _, sb := range queued {
		if _, err := s.place(sb); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) place(sb store.Sandbox) (bool, error) {
	candidates, err := s.candidates(sb.Tags)
	if err != nil {
		return false, err
	}
	hostID := s.policy.Pick(candidates)
	if hostID == "" {
		return false, nil
	}

	// Precedence, highest first: the sandbox's secret_env, its env, then the operator's
	// defaults. The operator's travel as secret_env because they may hold keys, and
	// nothing in secret_env is ever persisted.
	secret := maps.Clone(s.sandboxEnv)
	if secret == nil {
		secret = map[string]string{}
	}
	for k := range sb.Env {
		delete(secret, k)
	}
	maps.Copy(secret, s.secrets[sb.ID])

	spec := wire.Spec{
		SID:          sb.ID,
		Image:        sb.Image,
		Cmd:          sb.Cmd,
		IdleTimeoutS: sb.IdleTimeoutS,
		Env:          maps.Clone(sb.Env),
		SecretEnv:    secret,
	}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	if !s.hub.CreateSandbox(hostID, spec) {
		return false, nil
	}
	delete(s.secrets, sb.ID)
	if err := s.store.MarkCreating(sb.ID, hostID); err != nil {
		return false, err
	}
	s.log.Info("placed", "sid", sb.ID, "host_id", hostID)
	return true, nil
}

func (s *Scheduler) candidates(required []string) ([]Candidate, error) {
	hosts, err := s.store.Hosts()
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, h := range hosts {
		if h.Status != store.HostApproved || !HasTags(h.Tags, required) {
			continue
		}
		capacity, ok := s.hub.Capacity(h.ID)
		if !ok {
			continue
		}
		if free := capacity.Free(); free > 0 {
			out = append(out, Candidate{HostID: h.ID, Free: free})
		}
	}
	return out, nil
}

// reapUnknown ends sandboxes whose host never came back to claim them.
func (s *Scheduler) reapUnknown() {
	cutoff := time.Now().Add(-unknownGrace).UnixMilli()
	active, err := s.store.Active()
	if err != nil {
		s.log.Error("unknown sweep", "err", err)
		return
	}
	for _, sb := range active {
		if sb.UnknownSince == nil || *sb.UnknownSince >= cutoff {
			continue
		}
		err := s.store.MarkEnded(sb.ID, wire.EndLost,
			"host did not report this sandbox after control plane restart")
		if err != nil {
			s.log.Error("unknown sweep", "sid", sb.ID, "err", err)
		}
	}
}

// HasTags reports whether a host's set contains every tag a sandbox requires. Set
// containment is the whole rule — no operators, no wildcards (DESIGN.md decision 8).
func HasTags(host, required []string) bool {
	if len(required) == 0 {
		return true
	}
	has := make(map[string]struct{}, len(host))
	for _, t := range host {
		has[t] = struct{}{}
	}
	for _, t := range required {
		if _, ok := has[t]; !ok {
			return false
		}
	}
	return true
}
