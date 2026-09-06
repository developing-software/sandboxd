package hosts

import (
	"log/slog"
	"strings"

	"sandboxd/internal/adminapi"
	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
)

// Host admin use cases behind the operator API: list, approve, revoke. The view type is
// generated from api/admin.yaml, the document with no published client — so this is the
// one use case in the tree that is free to change shape whenever the operator is better
// served (DESIGN.md decision 27).

// What the admin API needs from the store, and what it needs from the hub. Both are
// narrow on purpose: approving a host is a row update plus one nudge to a waiting socket.
type (
	serviceStore interface {
		Hosts() ([]store.Host, error)
		Host(id string) (store.Host, bool, error)
		ApproveHost(id string) error
		RevokeHost(id string) error
	}
	presence interface {
		Online(hostID string) bool
		Capacity(hostID string) (cp.Capacity, bool)
		NotifyApproved(hostID string)
	}
)

type Service struct {
	store serviceStore
	hub   presence
	log   *slog.Logger
}

func NewService(s serviceStore, hub presence, log *slog.Logger) *Service {
	return &Service{store: s, hub: hub, log: log}
}

func (s *Service) List() ([]adminapi.HostView, error) {
	rows, err := s.store.Hosts()
	if err != nil {
		return nil, err
	}
	out := make([]adminapi.HostView, 0, len(rows))
	for _, h := range rows {
		v := adminapi.HostView{
			ID:     h.ID,
			Name:   h.Name,
			Status: adminapi.HostViewStatus(h.Status),
			Online: s.hub.Online(h.ID),
			// A host that reported no tags has an empty list, not a null one: the
			// document says `tags` is always an array (decision 23).
			Tags:       h.Tags,
			LastSeenAt: adminapi.NilInt64{Null: true},
			CreatedAt:  h.CreatedAt,
		}
		if v.Tags == nil {
			v.Tags = []string{}
		}
		if h.LastSeenAt != nil {
			v.LastSeenAt = adminapi.NewNilInt64(*h.LastSeenAt)
		}
		// The code is only meaningful, and only shown, while the host is waiting.
		if h.Status == store.HostPending {
			v.ApproveCode = adminapi.NewOptString(h.ApproveCode)
		}
		// Absent rather than null while the host is offline: `online` already says
		// whether there is a number to read (decision 23).
		if capacity, ok := s.hub.Capacity(h.ID); ok {
			v.Capacity = adminapi.NewOptCapacity(adminapi.Capacity{
				Running: capacity.Running, Max: capacity.Max,
			})
		}
		out = append(out, v)
	}
	return out, nil
}

// Approve promotes a pending host. The code is what the worker printed when it enrolled.
func (s *Service) Approve(id, code string) error {
	host, found, err := s.store.Host(id)
	if err != nil {
		return err
	}
	if !found {
		return cp.NotFound("host")
	}
	if host.Status != store.HostPending {
		return cp.Conflict("host is %s", host.Status)
	}
	if code == "" || strings.ToUpper(code) != host.ApproveCode {
		return cp.Invalid("approval code does not match")
	}
	if err := s.store.ApproveHost(host.ID); err != nil {
		return err
	}
	s.log.Info("host approved", "host_id", host.ID, "name", host.Name)
	// Promote the socket that is already waiting, so the worker does not have to reconnect.
	s.hub.NotifyApproved(host.ID)
	return nil
}

func (s *Service) Revoke(id string) error {
	_, found, err := s.store.Host(id)
	if err != nil {
		return err
	}
	if !found {
		return cp.NotFound("host")
	}
	return s.store.RevokeHost(id)
}
