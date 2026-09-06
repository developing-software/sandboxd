package hosts

import (
	"crypto/subtle"
	"fmt"
	"slices"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// Enrollment maps a hello onto a host row and decides what the worker hears back. An
// unknown fingerprint becomes a pending row with a human code; approved is accepted;
// revoked is rejected. A matching join token approves on the spot, and a wrong one is not
// a rejection — the worker simply falls back to the printed code.

type enrollKind int

const (
	accepted enrollKind = iota
	pending
	rejected
)

type enrollment struct {
	kind enrollKind
	host store.Host
	// joined is set when the join token, not an admin, did the approving.
	joined bool
	isNew  bool
	// badToken means a token was offered and did not match: worth a log line, not a
	// rejection, because the printed code still works.
	badToken bool
	reason   string
}

// What enrollment needs from the store. The host admin API declares its own.
type enrollStore interface {
	Host(id string) (store.Host, bool, error)
	HostByFingerprint(fp string) (store.Host, bool, error)
	InsertPendingHost(h store.Host) error
	ApproveHost(id string) error
	TouchHost(id string, p store.HostPatch) error
}

// ProviderTag is what every host behind the tunnel carries, added here rather than by the
// worker: which provider a host belongs to is the control plane's fact, and a sandbox
// selects one with `tags: [provider:workers]` the way it selects anything else.
const ProviderTag = "provider:workers"

func enroll(s enrollStore, hello *wire.Hello, joinToken string) (enrollment, error) {
	joined := tokenMatches(joinToken, hello.JoinToken)
	tags := hello.Tags
	if !slices.Contains(tags, ProviderTag) {
		tags = slices.Sorted(slices.Values(append(slices.Clone(tags), ProviderTag)))
	}

	host, found, err := s.HostByFingerprint(hello.Fingerprint)
	if err != nil {
		return enrollment{}, err
	}
	isNew := !found
	if !found {
		host = store.Host{
			ID:           wire.NewHostID(),
			Name:         hello.Name,
			Fingerprint:  hello.Fingerprint,
			Status:       store.HostPending,
			ApproveCode:  wire.NewCode(),
			MaxSandboxes: hello.MaxSandboxes,
			Tags:         tags,
		}
		if err := s.InsertPendingHost(host); err != nil {
			return enrollment{}, err
		}
	}
	// The tags and the capacity are refreshed on every hello, so GET /hosts and the
	// unsatisfiable-tags check see what this worker reports today.
	patch := store.HostPatch{Name: &hello.Name, MaxSandboxes: &hello.MaxSandboxes, Tags: tags}
	if err := s.TouchHost(host.ID, patch); err != nil {
		return enrollment{}, err
	}
	host.Name, host.MaxSandboxes, host.Tags = hello.Name, hello.MaxSandboxes, tags

	switch {
	case host.Status == store.HostRevoked:
		return enrollment{kind: rejected, host: host, reason: "revoked"}, nil
	case host.Status == store.HostApproved:
		return enrollment{kind: accepted, host: host}, nil
	case !joined:
		return enrollment{
			kind: pending, host: host, isNew: isNew, badToken: hello.JoinToken != "",
		}, nil
	}

	if err := s.ApproveHost(host.ID); err != nil {
		return enrollment{}, err
	}
	approved, found, err := s.Host(host.ID)
	if err != nil {
		return enrollment{}, err
	}
	if !found {
		return enrollment{}, fmt.Errorf("hosts: host %s vanished during enrollment", host.ID)
	}
	return enrollment{kind: accepted, host: approved, joined: true}, nil
}

func tokenMatches(expected, given string) bool {
	if expected == "" || given == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(given)) == 1
}
