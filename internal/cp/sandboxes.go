package cp

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// Sandbox use cases behind the HTTP API. huma has already shaped the body; what is left
// is the semantics: splitting secrets from what is stored, refusing a tag combination the
// fleet cannot serve, and ownership — the control plane has no user table, so `owner_id`
// is the whole model.

const (
	attachTTL  = 60 * time.Second
	previewTTL = 10 * time.Minute
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

func (s *Sandboxes) Create(ctx context.Context, b CreateSandbox) (SandboxView, error) {
	if err := ValidateEnv("env", b.Env); err != nil {
		return SandboxView{}, err
	}
	if err := ValidateEnv("secret_env", b.SecretEnv); err != nil {
		return SandboxView{}, err
	}
	if err := s.satisfiable(b.Tags); err != nil {
		return SandboxView{}, err
	}

	idle := b.IdleTimeoutS
	if idle == 0 {
		idle = DefaultIdleS
	}
	sb := store.Sandbox{
		ID:           wire.NewSandboxID(),
		OwnerID:      b.OwnerID,
		Status:       store.Queued,
		Image:        strings.TrimSpace(b.Image),
		Cmd:          b.Cmd,
		Env:          b.Env,
		Tags:         b.Tags,
		IdleTimeoutS: idle,
		CreatedAt:    time.Now().UnixMilli(),
	}
	// The scheduler writes the row and answers once it has placed or queued it, so the
	// view below is already `creating` when a host had room.
	if err := s.sched.Submit(ctx, sb, b.SecretEnv); err != nil {
		return SandboxView{}, err
	}
	return s.one(sb.ID)
}

// List is newest first. An empty ownerID lists every owner's.
func (s *Sandboxes) List(ownerID string) ([]SandboxView, error) {
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
	out := make([]SandboxView, 0, len(rows))
	for _, sb := range rows {
		out = append(out, s.view(sb, queue))
	}
	return out, nil
}

func (s *Sandboxes) Get(sid, ownerID string) (SandboxView, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return SandboxView{}, err
	}
	return s.viewOf(sb)
}

// Cancel ends a sandbox and returns it as it stands afterwards: ended for a queued or
// hostless one, still running while its host is being told.
func (s *Sandboxes) Cancel(ctx context.Context, sid, ownerID string) (SandboxView, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return SandboxView{}, err
	}
	if err := s.sched.Cancel(ctx, sb.ID); err != nil {
		return SandboxView{}, err
	}
	return s.one(sb.ID)
}

func (s *Sandboxes) AttachToken(sid, ownerID string) (AttachToken, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return AttachToken{}, err
	}
	if sb.Status != store.Running {
		return AttachToken{}, Conflict("sandbox is %s", sb.Status)
	}
	token := s.tokens.Sign(TokenPayload{Kind: TokenAttach, SID: sb.ID}, attachTTL)
	ws := "ws" + strings.TrimPrefix(s.publicURL, "http")
	return AttachToken{
		Token:      token,
		WssURL:     ws + "/attach?token=" + url.QueryEscape(token),
		ExpiresInS: int(attachTTL.Seconds()),
	}, nil
}

func (s *Sandboxes) PreviewToken(sid, ownerID string, port int) (PreviewToken, error) {
	sb, err := s.owned(sid, ownerID)
	if err != nil {
		return PreviewToken{}, err
	}
	if port < 1 || port > 65535 {
		return PreviewToken{}, Invalid("port must be 1-65535")
	}
	pub, err := url.Parse(s.publicURL)
	if err != nil {
		return PreviewToken{}, fmt.Errorf("cp: public url: %w", err)
	}
	token := s.tokens.Sign(TokenPayload{Kind: TokenPreview, SID: sb.ID, Port: port}, previewTTL)
	suffix := ""
	if p := pub.Port(); p != "" {
		suffix = ":" + p
	}
	return PreviewToken{
		Token: token,
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
// from a missing one (SPEC.md, "Auth").
func (s *Sandboxes) owned(sid, ownerID string) (store.Sandbox, error) {
	if ownerID == "" {
		return store.Sandbox{}, Invalid("owner_id is required")
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

func (s *Sandboxes) one(sid string) (SandboxView, error) {
	sb, found, err := s.store.Sandbox(sid)
	if err != nil {
		return SandboxView{}, err
	}
	if !found {
		return SandboxView{}, NotFound("sandbox")
	}
	return s.viewOf(sb)
}

func (s *Sandboxes) viewOf(sb store.Sandbox) (SandboxView, error) {
	queue := map[string]int{}
	if sb.Status == store.Queued {
		var err error
		if queue, err = s.queue(); err != nil {
			return SandboxView{}, err
		}
	}
	return s.view(sb, queue), nil
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

func (s *Sandboxes) view(sb store.Sandbox, queue map[string]int) SandboxView {
	v := SandboxView{
		ID:           sb.ID,
		OwnerID:      sb.OwnerID,
		Status:       sb.Status,
		HostID:       sb.HostID,
		Image:        sb.Image,
		Cmd:          sb.Cmd,
		Env:          sb.Env,
		Tags:         sb.Tags,
		IdleTimeoutS: sb.IdleTimeoutS,
		CreatedAt:    sb.CreatedAt,
		StartedAt:    sb.StartedAt,
		EndedAt:      sb.EndedAt,
	}
	if sb.HostID != nil {
		online := s.hosts.Online(*sb.HostID)
		v.HostOnline = &online
	}
	if pos, ok := queue[sb.ID]; ok && sb.Status == store.Queued {
		v.QueuePosition = &pos
	}
	if sb.EndedReason != "" {
		reason := sb.EndedReason
		v.EndedReason = &reason
	}
	if sb.EndedDetail != "" {
		detail := sb.EndedDetail
		v.EndedDetail = &detail
	}
	if v.Env == nil {
		v.Env = map[string]string{}
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return v
}
