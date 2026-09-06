package server

import (
	"context"
	"errors"
	"log/slog"

	"sandboxd/internal/gen/clientapi"
)

// The client contract's handler: `api/client.yaml` → `internal/gen/clientapi` → this.
//
// Every method here is a forward. The decoding, the validation and the status of a
// successful response are generated from the document, and the status of a failure is
// decided in errors.go — so there is nothing left for a handler to do but call one use
// case, which is what makes the compiler enough to keep this file honest.
type sandboxHandler struct {
	sandboxes sandboxAPI
	log       *slog.Logger
}

var _ clientapi.Handler = (*sandboxHandler)(nil)

func (h *sandboxHandler) Healthz(context.Context) (*clientapi.Health, error) {
	return &clientapi.Health{Ok: true}, nil
}

func (h *sandboxHandler) CreateSandbox(
	ctx context.Context, req *clientapi.CreateSandbox, p clientapi.CreateSandboxParams,
) (*clientapi.SandboxView, error) {
	return h.sandboxes.Create(ctx, p.XSandboxdOwner, req)
}

func (h *sandboxHandler) ListSandboxes(
	_ context.Context, p clientapi.ListSandboxesParams,
) ([]clientapi.SandboxView, error) {
	return h.sandboxes.List(p.XSandboxdOwner)
}

func (h *sandboxHandler) GetSandbox(
	_ context.Context, p clientapi.GetSandboxParams,
) (*clientapi.SandboxView, error) {
	return h.sandboxes.Get(p.ID, p.XSandboxdOwner)
}

func (h *sandboxHandler) EndSandbox(
	ctx context.Context, p clientapi.EndSandboxParams,
) (*clientapi.SandboxView, error) {
	return h.sandboxes.Cancel(ctx, p.ID, p.XSandboxdOwner)
}

func (h *sandboxHandler) OpenTerminal(
	_ context.Context, p clientapi.OpenTerminalParams,
) (*clientapi.Link, error) {
	return h.sandboxes.Terminal(p.ID, p.XSandboxdOwner)
}

func (h *sandboxHandler) OpenPreview(
	_ context.Context, req *clientapi.PreviewBody, p clientapi.OpenPreviewParams,
) (*clientapi.Link, error) {
	return h.sandboxes.Preview(p.ID, p.XSandboxdOwner, req.Port)
}

// errNotRouted is unreachable, and says so if it ever is not.
var errNotRouted = errors.New("http: the terminal socket is served by net/http, not here")

// AttachTerminal is never called: `New` registers `GET /sandboxes/{id}/terminal` on the
// mux ahead of the generated router, because an upgrade needs the ResponseWriter this
// signature has not got. The method exists because the operation is in the document, and
// the compiler is what keeps those two facts from drifting apart — delete the operation
// and this stops building.
func (h *sandboxHandler) AttachTerminal(context.Context, clientapi.AttachTerminalParams) error {
	return errNotRouted
}
