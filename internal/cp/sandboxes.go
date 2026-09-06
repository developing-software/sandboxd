package cp

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/openapi"
	"sandboxd/internal/wire"
)

// Sandbox use cases behind the HTTP API. The generated decoder has already shaped and
// validated the body — `image` is present, `cmd` is not empty, `idle_timeout_s` is above
// the floor, and no unknown key got through — so what is left here is the semantics:
// splitting secrets from what is stored, refusing a tag combination the fleet cannot
// serve, and ownership. The control plane has no user table, so the owner is the whole
// model, and it arrives as a header (DESIGN.md decision 24).
//
// The request and response types are the generated ones, from `api/client.yaml`. There is
// no second set of structs to keep in agreement with the document (decision 18).

const (
	terminalTTL = 60 * time.Second
	previewTTL  = 10 * time.Minute
	// DefaultIdleS applies when a caller names no timeout. The floor a caller may ask for
	// is `minimum: 60` in the document, and the generated validator is what enforces it.
	DefaultIdleS = 1800
)

// What the sandbox use cases need. Hosts are read for one reason only: to tell a
// requirement that must wait from one that can never be met.
type (
	sandboxStore interface {
		Sandbox(id string) (store.Sandbox, bool, error)
		Sandboxes(ownerID string) ([]store.Sandbox, error)
		Queued() ([]store.Sandbox, error)
		Hosts() ([]store.Host, error)
	}
	onlineHosts interface {
		Online(hostID string) bool
	}
	submitter interface {
		Submit(ctx context.Context, sb store.Sandbox, secretEnv map[string]string) error
		Cancel(ctx context.Context, sid string) error
	}
)

type Sandboxes struct {
	publicURL     string
	previewDomain string
	store         sandboxStore
	hosts         onlineHosts
	sched         submitter
	tokens        *Tokens
}

func NewSandboxes(
	cfg Config, s sandboxStore, hosts onlineHosts, sched submitter, tokens *Tokens,
) *Sandboxes {
	return &Sandboxes{
		publicURL:     cfg.PublicURL,
		previewDomain: cfg.PreviewDomain,
		store:         s, hosts: hosts, sched: sched, tokens: tokens,
	}
}

func (s *Sandboxes) Create(
	ctx context.Context, ownerID string, b *openapi.CreateSandbox,
) (*openapi.SandboxView, error) {
	env := map[string]string(b.Env.Or(nil))
	secretEnv := map[string]string(b.SecretEnv.Or(nil))
	if err := ValidateEnv("env", env); err != nil {
		return nil, err
	}
	if err := ValidateEnv("secret_env", secretEnv); err != nil {
		return nil, err
	}
	if err := s.satisfiable(b.Tags); err != nil {
		return nil, err
	}

	sb := store.Sandbox{
		ID:           wire.NewSandboxID(),
		OwnerID:      ownerID,
		Status:       store.Queued,
		Image:        strings.TrimSpace(b.Image),
		Cmd:          b.Cmd,
		Env:          env,
		Tags:         b.Tags,
		IdleTimeoutS: b.IdleTimeoutS.Or(DefaultIdleS),
		CreatedAt:    time.Now().UnixMilli(),
	}
	// The scheduler writes the row and answers once it has placed or queued it, so the
	// view below is already `creating` when a host had room.
	if err := s.sched.Submit(ctx, sb, secretEnv); err != nil {
		return nil, err
	}
	return s.one(sb.ID)
}

// List is this owner's, newest first. There is no unscoped listing: the header the owner
// arrives in is required on every route here, so an empty one never reaches this.
func (s *Sandboxes) List(ownerID string) ([]openapi.SandboxView, error) {
	rows, err := s.store.Sandboxes(ownerID)
	if err != nil {
		return nil, err
	}
	// One queue snapshot for the whole response. The TypeScript control plane rescanned
	// the queued table once per queued row (PLAN.md, "Bugs to fix in the port, not carry").
	queue, err := s.queue()
	if err != nil {
		return nil, err
	}
	out := make([]openapi.SandboxView, 0, len(rows))
	for _, sb := range rows {
		out = append(out, s.view(sb, queue))
	}
	return out, nil
}

func (s *Sandboxes) Get(sid, ownerID string) (*openapi.SandboxView, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return nil, err
	}
	return s.viewOf(sb)
}

// Cancel ends a sandbox and returns it as it stands afterwards: ended for a queued or
// hostless one, still running while its host is being told.
func (s *Sandboxes) Cancel(ctx context.Context, sid, ownerID string) (*openapi.SandboxView, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return nil, err
	}
	if err := s.sched.Cancel(ctx, sb.ID); err != nil {
		return nil, err
	}
	return s.one(sb.ID)
}

// Terminal mints the URL a browser is upgraded on. The token rides inside it, because
// handing the URL to a browser was always the caller's only move (decision 26).
func (s *Sandboxes) Terminal(sid, ownerID string) (*openapi.Link, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return nil, err
	}
	if sb.Status != store.Running {
		return nil, Conflict("sandbox is %s", sb.Status)
	}
	token := s.tokens.Sign(TokenPayload{Kind: TokenAttach, SID: sb.ID}, terminalTTL)
	ws := "ws" + strings.TrimPrefix(s.publicURL, "http")
	return &openapi.Link{
		URL:        fmt.Sprintf("%s/sandboxes/%s/terminal?token=%s", ws, sb.ID, url.QueryEscape(token)),
		ExpiresInS: int(terminalTTL.Seconds()),
	}, nil
}

// Preview mints the URL one port inside the sandbox is reachable at. The first request
// there trades the token for a subdomain-scoped cookie, so it does not stay in the bar.
func (s *Sandboxes) Preview(sid, ownerID string, port int) (*openapi.Link, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return nil, err
	}
	pub, err := url.Parse(s.publicURL)
	if err != nil {
		return nil, fmt.Errorf("cp: public url: %w", err)
	}
	token := s.tokens.Sign(TokenPayload{Kind: TokenPreview, SID: sb.ID, Port: port}, previewTTL)
	suffix := ""
	if p := pub.Port(); p != "" {
		suffix = ":" + p
	}
	return &openapi.Link{
		URL: fmt.Sprintf("%s://%d-%s.%s%s/?t=%s",
			pub.Scheme, port, sb.ID, s.previewDomain, suffix, url.QueryEscape(token)),
		ExpiresInS: int(previewTTL.Seconds()),
	}, nil
}

// Sandbox is what the attach bridge and the preview proxy look a sandbox up with: the
// token has already said which one, so there is no owner to check.
func (s *Sandboxes) Sandbox(sid string) (store.Sandbox, bool, error) {
	return s.store.Sandbox(sid)
}

// owned is the ownership check. A sandbox belonging to another owner is indistinguishable
// from a missing one (SPEC.md, "Auth"). The empty check is not the transport's job done
// twice: a use case that trusts a header for ownership is one route away from a bug.
func (s *Sandboxes) owned(sid, ownerID string) (store.Sandbox, error) {
	if ownerID == "" {
		return store.Sandbox{}, Invalid("an owner is required")
	}
	sb, found, err := s.store.Sandbox(sid)
	if err != nil {
		return store.Sandbox{}, err
	}
	if !found || sb.OwnerID != ownerID {
		return store.Sandbox{}, NotFound("sandbox")
	}
	return sb, nil
}

// satisfiable separates "no host can ever serve this" from "no host is free right now".
// The first is a 422 at create, because queueing it forever would hide a typo.
func (s *Sandboxes) satisfiable(required []string) error {
	if len(required) == 0 {
		return nil
	}
	hosts, err := s.store.Hosts()
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h.Status == store.HostApproved && HasTags(h.Tags, required) {
			return nil
		}
	}
	return Unsatisfiable("no approved host carries all of %s", strings.Join(required, ", "))
}

func (s *Sandboxes) one(sid string) (*openapi.SandboxView, error) {
	sb, found, err := s.store.Sandbox(sid)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, NotFound("sandbox")
	}
	return s.viewOf(sb)
}

func (s *Sandboxes) viewOf(sb store.Sandbox) (*openapi.SandboxView, error) {
	queue := map[string]int{}
	if sb.Status == store.Queued {
		var err error
		if queue, err = s.queue(); err != nil {
			return nil, err
		}
	}
	v := s.view(sb, queue)
	return &v, nil
}

// queue is the 1-based position of every queued sandbox, from one scan.
func (s *Sandboxes) queue() (map[string]int, error) {
	rows, err := s.store.Queued()
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for i, sb := range rows {
		out[sb.ID] = i + 1
	}
	return out, nil
}

func (s *Sandboxes) view(sb store.Sandbox, queue map[string]int) openapi.SandboxView {
	v := openapi.SandboxView{
		ID:      sb.ID,
		OwnerID: sb.OwnerID,
		Status:  openapi.SandboxViewStatus(sb.Status),
		HostID:  nilString(sb.HostID),
		Image:   sb.Image,
		// A nil slice or map would marshal as `null`. The document says these are always
		// there, so a client — generated or not — never has to unwrap one (decision 23).
		Cmd:          orEmpty(sb.Cmd),
		Env:          openapi.SandboxViewEnv(sb.Env),
		Tags:         orEmpty(sb.Tags),
		IdleTimeoutS: sb.IdleTimeoutS,
		CreatedAt:    sb.CreatedAt,
		StartedAt:    nilInt64(sb.StartedAt),
		EndedAt:      nilInt64(sb.EndedAt),
	}
	if v.Env == nil {
		v.Env = openapi.SandboxViewEnv{}
	}
	if sb.HostID != nil {
		v.HostOnline = openapi.NewNilBool(s.hosts.Online(*sb.HostID))
	} else {
		v.HostOnline.SetToNull()
	}
	v.QueuePosition.SetToNull()
	if pos, ok := queue[sb.ID]; ok && sb.Status == store.Queued {
		v.QueuePosition = openapi.NewNilInt(pos)
	}
	v.EndedReason.SetToNull()
	if sb.EndedReason != "" {
		v.EndedReason = openapi.NewNilSandboxViewEndedReason(
			openapi.SandboxViewEndedReason(sb.EndedReason))
	}
	v.EndedDetail.SetToNull()
	if sb.EndedDetail != "" {
		v.EndedDetail = openapi.NewNilString(sb.EndedDetail)
	}
	return v
}

// A Go pointer is how the store says "no value"; the generated `NilT` is how the document
// does. These three are the whole translation.

func nilString(p *string) openapi.NilString {
	if p == nil {
		return openapi.NilString{Null: true}
	}
	return openapi.NewNilString(*p)
}

func nilInt64(p *int64) openapi.NilInt64 {
	if p == nil {
		return openapi.NilInt64{Null: true}
	}
	return openapi.NewNilInt64(*p)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
