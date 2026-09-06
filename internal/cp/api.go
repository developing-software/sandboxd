package cp

import (
	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// Every model the JSON API speaks, in one file — the replacement for the TypeScript
// `schema.ts`. The struct tags are the OpenAPI document: `cp/http` registers these types
// and huma derives the schema, so a field documented here is documented once.
//
// Response fields are pointers rather than `omitempty` where SPEC.md calls them nullable:
// a client reads `"queue_position": null` and a missing key differently.

const (
	// MinIdleS is the floor a caller may ask for; DefaultIdleS applies when they do not.
	MinIdleS     = 60
	DefaultIdleS = 1800
)

// CreateSandbox is the POST /sandboxes body. Unknown keys are rejected: a `repo` or
// `preset` sent here means the caller meant their own client (SPEC.md, "Error").
type CreateSandbox struct {
	OwnerID string   `json:"owner_id" minLength:"1" example:"user_42" doc:"The parent app's user. Every later call must present the same value."`
	Image   string   `json:"image"    minLength:"1" example:"sandboxd-coding-agent:latest"`
	Cmd     []string `json:"cmd,omitempty" minItems:"1" doc:"Exec'd in the PTY. Omit for the image's default entry."`
	// IdleTimeoutS is validated by huma against the same floor the store records.
	IdleTimeoutS int               `json:"idle_timeout_s,omitempty" minimum:"60" doc:"Ended after this long without PTY traffic. Default 1800."`
	Env          map[string]string `json:"env,omitempty" doc:"Persisted, visible in the API."`
	SecretEnv    map[string]string `json:"secret_env,omitempty" doc:"Forwarded to the worker, never persisted or shown."`
	Tags         []string          `json:"tags,omitempty" doc:"Host tags this sandbox requires. Only hosts carrying all of them are candidates."`
}

// SandboxView is a sandbox as the API returns it. Secrets never appear here.
type SandboxView struct {
	ID            string              `json:"id" example:"s_ab12cd34"`
	OwnerID       string              `json:"owner_id"`
	Status        store.SandboxStatus `json:"status" enum:"queued,creating,running,ended"`
	HostID        *string             `json:"host_id"`
	HostOnline    *bool               `json:"host_online" doc:"Live, not stored."`
	QueuePosition *int                `json:"queue_position" doc:"1-based, only while queued."`
	EndedReason   *wire.EndReason     `json:"ended_reason" enum:"closed,idle,failed,lost,exited"`
	EndedDetail   *string             `json:"ended_detail"`
	Image         string              `json:"image"`
	Cmd           []string            `json:"cmd"`
	Env           map[string]string   `json:"env"`
	Tags          []string            `json:"tags"`
	IdleTimeoutS  int                 `json:"idle_timeout_s"`
	CreatedAt     int64               `json:"created_at"`
	StartedAt     *int64              `json:"started_at"`
	EndedAt       *int64              `json:"ended_at"`
}

// Capacity is what a host reports about itself, live. Nil when the host is offline.
type Capacity struct {
	Running int `json:"running"`
	Max     int `json:"max"`
}

// Free is the slot count placement bids with.
func (c Capacity) Free() int { return c.Max - c.Running }

// HostView is a worker host as the API returns it.
type HostView struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Status      store.HostStatus `json:"status" enum:"pending,approved,revoked"`
	ApproveCode string           `json:"approve_code,omitempty" doc:"Only while pending."`
	Online      bool             `json:"online" doc:"Tunnel connected right now."`
	// Absent rather than null, the one exception to the rule above: huma refuses to type
	// a nullable object reference, so `"capacity": null` would reach a generated client
	// as a non-null field. `online` already says whether there is a number to read.
	Capacity *Capacity `json:"capacity,omitempty" doc:"Only while online."`
	Tags        []string         `json:"tags" doc:"What this host reported in its last hello."`
	LastSeenAt  *int64           `json:"last_seen_at"`
	CreatedAt   int64            `json:"created_at"`
}

// AttachToken opens one browser terminal on one sandbox.
type AttachToken struct {
	Token      string `json:"token"`
	WssURL     string `json:"wss_url"`
	ExpiresInS int    `json:"expires_in_s"`
}

// PreviewToken is traded for a cookie by the first request to the preview host.
type PreviewToken struct {
	Token      string `json:"token"`
	URL        string `json:"url"`
	ExpiresInS int    `json:"expires_in_s"`
}
