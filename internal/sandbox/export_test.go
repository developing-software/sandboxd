package sandbox

import (
	"context"
	"time"
)

// The few internals the external tests reach for: the reaper's tick, the idle clock, the
// ring, and whether a secret survived the hand-off. Exported here rather than from the
// package so the API stays what its two owners need.

func (m *Manager) ReapIdle(ctx context.Context) { m.reapIdle(ctx) }

// Backdate ages a sandbox's idle clock, so a timeout can be tested without waiting for it.
func (m *Manager) Backdate(sid string, by time.Duration) {
	fan := m.box(sid).fan
	fan.mu.Lock()
	fan.last = time.Now().Add(-by)
	fan.mu.Unlock()
}

func (m *Manager) Tail(sid string) []byte { return m.box(sid).fan.ring.Snapshot() }

func (m *Manager) SecretsKept(sid string) bool {
	box := m.box(sid)
	box.mu.Lock()
	defer box.mu.Unlock()
	return box.spec.SecretEnv != nil
}

func (m *Manager) box(sid string) *sandbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.boxes[sid]
}
