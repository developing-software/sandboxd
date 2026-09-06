package http

import (
	"context"
	"log/slog"

	"sandboxd/internal/adminapi"
)

// The operator surface's handler: `api/admin.yaml` → `internal/adminapi` → this. Approve
// and revoke return nothing but an error, because the document answers them 204: a
// mutation with nothing to say says it with the status (DESIGN.md decision 25).
type hostHandler struct {
	hosts hostAPI
	log   *slog.Logger
}

var _ adminapi.Handler = (*hostHandler)(nil)

func (h *hostHandler) ListHosts(context.Context) ([]adminapi.HostView, error) {
	return h.hosts.List()
}

func (h *hostHandler) ApproveHost(
	_ context.Context, req *adminapi.ApproveBody, p adminapi.ApproveHostParams,
) error {
	return h.hosts.Approve(p.ID, req.Code)
}

func (h *hostHandler) RevokeHost(_ context.Context, p adminapi.RevokeHostParams) error {
	return h.hosts.Revoke(p.ID)
}
