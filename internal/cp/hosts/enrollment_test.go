package hosts

import (
	"log/slog"
	"regexp"
	"slices"
	"testing"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func openStore(t *testing.T) *store.SQLite {
	t.Helper()
	s, err := store.Open(":memory:", discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func hello(fingerprint string, opts ...func(*wire.Hello)) *wire.Hello {
	h := &wire.Hello{
		Name:         "box",
		Fingerprint:  fingerprint,
		Running:      []string{},
		MaxSandboxes: 3,
		Tags:         []string{"arch:amd64", "os:linux", "driver:docker"},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

func named(name string) func(*wire.Hello) {
	return func(h *wire.Hello) { h.Name = name }
}

func joining(token string) func(*wire.Hello) {
	return func(h *wire.Hello) { h.JoinToken = token }
}

var codeFormat = regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{2}$`)

func TestAnUnknownFingerprintBecomesAPendingRow(t *testing.T) {
	s := openStore(t)

	first, err := enroll(s, hello("fp1"), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.kind != pending || !first.isNew || first.badToken {
		t.Fatalf("first hello = %+v", first)
	}
	if !codeFormat.MatchString(first.host.ApproveCode) {
		t.Errorf("code = %q, want the format a human types", first.host.ApproveCode)
	}
	// Plus the provider tag, which is the control plane's fact, not the worker's.
	if !slices.Equal(first.host.Tags, []string{"arch:amd64", "driver:docker", "os:linux", ProviderTag}) {
		t.Errorf("tags = %v", first.host.Tags)
	}

	// The same fingerprint is the same host, and a hello refreshes what it reports.
	again, err := enroll(s, hello("fp1", named("renamed")), "")
	if err != nil {
		t.Fatal(err)
	}
	if again.kind != pending || again.isNew || again.host.ID != first.host.ID {
		t.Fatalf("second hello = %+v", again)
	}
	row, _, _ := s.Host(first.host.ID)
	if row.Name != "renamed" {
		t.Errorf("name = %q, want the hello to have refreshed it", row.Name)
	}
}

func TestApprovedIsAcceptedAndRevokedIsRejected(t *testing.T) {
	s := openStore(t)
	e, err := enroll(s, hello("fp"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveHost(e.host.ID); err != nil {
		t.Fatal(err)
	}
	got, err := enroll(s, hello("fp"), "")
	if err != nil || got.kind != accepted || got.joined {
		t.Fatalf("approved host = %+v, %v", got, err)
	}

	if err := s.RevokeHost(e.host.ID); err != nil {
		t.Fatal(err)
	}
	got, err = enroll(s, hello("fp"), "")
	if err != nil || got.kind != rejected || got.reason != "revoked" {
		t.Fatalf("revoked host = %+v, %v", got, err)
	}
}

func TestJoinTokenApprovesOnTheSpot(t *testing.T) {
	s := openStore(t)
	got, err := enroll(s, hello("fp", joining("jt")), "jt")
	if err != nil {
		t.Fatal(err)
	}
	if got.kind != accepted || !got.joined {
		t.Fatalf("= %+v", got)
	}
	row, _, _ := s.Host(got.host.ID)
	if row.Status != store.HostApproved || row.ApproveCode != "" {
		t.Errorf("row = %+v, want approved with no code left", row)
	}

	// The next hello is an ordinary accepted one, token or not.
	again, err := enroll(s, hello("fp"), "jt")
	if err != nil || again.kind != accepted || again.joined {
		t.Errorf("second hello = %+v, %v", again, err)
	}
}

func TestJoinTokenAlsoApprovesAHostAlreadyWaiting(t *testing.T) {
	s := openStore(t)
	waiting, err := enroll(s, hello("fp"), "jt")
	if err != nil || waiting.kind != pending {
		t.Fatalf("= %+v, %v", waiting, err)
	}
	got, err := enroll(s, hello("fp", joining("jt")), "jt")
	if err != nil {
		t.Fatal(err)
	}
	if got.kind != accepted || got.host.ID != waiting.host.ID {
		t.Errorf("= %+v, want the same host approved", got)
	}
}

func TestAWrongJoinTokenFallsBackToTheCode(t *testing.T) {
	s := openStore(t)
	// Offered and wrong: worth flagging, but the printed code still works.
	wrong, err := enroll(s, hello("fp", joining("nope")), "jt")
	if err != nil {
		t.Fatal(err)
	}
	if wrong.kind != pending || !wrong.badToken {
		t.Errorf("wrong token = %+v", wrong)
	}
	// Offered when the control plane has none configured: inert, not an error.
	inert, err := enroll(s, hello("fp2", joining("jt")), "")
	if err != nil {
		t.Fatal(err)
	}
	if inert.kind != pending || !inert.badToken {
		t.Errorf("unexpected token = %+v", inert)
	}
	// And a token never resurrects a revoked host.
	if err := s.RevokeHost(wrong.host.ID); err != nil {
		t.Fatal(err)
	}
	got, err := enroll(s, hello("fp", joining("jt")), "jt")
	if err != nil || got.kind != rejected {
		t.Errorf("revoked host with a good token = %+v, %v", got, err)
	}
}
