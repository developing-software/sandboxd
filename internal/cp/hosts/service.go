package hosts

import (
	"log/slog"
	"strings"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
)

// Host admin use cases behind the HTTP API: list, approve, revoke.

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

func (s *Service) List() ([]cp.HostView, error) {
	rows, err := s.store.Hosts()
	if err != nil {
		return nil, err
	}
	out := make([]cp.HostView, 0, len(rows))
	for _, h := range rows {
		v := cp.HostView{
			ID:         h.ID,
			Name:       h.Name,
			Status:     h.Status,
			Online:     s.hub.Online(h.ID),
			Tags:       h.Tags,
			LastSeenAt: h.LastSeenAt,
			CreatedAt:  h.CreatedAt,
		}
		// The code is only meaningful, and only shown, while the host is waiting.
		if h.Status == store.HostPending {
			v.ApproveCode = h.ApproveCode
		}
		if capacity, ok := s.hub.Capacity(h.ID); ok {
			v.Capacity = &capacity
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
