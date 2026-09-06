// Package store is the control plane's state: one SQLite file, no migrations. It is a
// leaf of the cp tree — everything else imports these structs, and only this package
// imports a SQL driver.
//
// Rows are disposable by design (DESIGN.md, "Storage"): sandboxes are ephemeral and hosts
// re-enroll, so a database at another schema version is dropped rather than migrated.
package store

import "sandboxd/internal/wire"

type (
	HostStatus    string
	SandboxStatus string
)

const (
	HostPending  HostStatus = "pending"
	HostApproved HostStatus = "approved"
	HostRevoked  HostStatus = "revoked"
)

const (
	Queued   SandboxStatus = "queued"
	Creating SandboxStatus = "creating"
	Running  SandboxStatus = "running"
	Ended    SandboxStatus = "ended"
)

// Host is a worker that has said hello at least once.
type Host struct {
	ID          string
	Name        string
	Fingerprint string
	Status      HostStatus
	// ApproveCode is what the worker printed. Empty once approved.
	ApproveCode  string
	MaxSandboxes int
	// Tags caches the last hello's set, so the unsatisfiable-tags check and GET /hosts
	// both work while the host is offline.
	Tags       []string
	LastSeenAt *int64
	CreatedAt  int64
}

// Sandbox is a row as the rest of the CP sees it: JSON columns already decoded.
//
// Secrets are absent on purpose. `secret_env` reaches the scheduler's memory and the
// worker, and never a column here (DESIGN.md decision 7).
type Sandbox struct {
	ID      string
	OwnerID string
	// HostID is nil until the sandbox is placed.
	HostID *string
	Status SandboxStatus
	// EndedReason is empty while the sandbox is not ended.
	EndedReason wire.EndReason
	EndedDetail string
	Image       string
	// Cmd nil means the image's own default entry.
	Cmd []string
	Env map[string]string
	// Tags is the requirement, not the host's set. Kept because placement can happen
	// long after create.
	Tags         []string
	IdleTimeoutS int
	CreatedAt    int64
	StartedAt    *int64
	EndedAt      *int64
	// UnknownSince is set on CP boot for active sandboxes whose host has not re-reported
	// them yet; the scheduler ends them if the grace period passes.
	UnknownSince *int64
}

// HostPatch is what a hello or a heartbeat refreshes. A nil field is left alone, which is
// why a heartbeat can report capacity without claiming to know the name or the tags.
type HostPatch struct {
	Name         *string
	MaxSandboxes *int
	Tags         []string
}
