package store

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sandboxd/internal/wire"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func open(t *testing.T) *SQLite {
	t.Helper()
	s, err := Open(":memory:", discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAnOldSchemaIsWipedNotRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.db")

	// A control plane from before the rename: the old table name, a column this version
	// does not have, and the version it was written at.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`
		CREATE TABLE hosts (id TEXT PRIMARY KEY);
		CREATE TABLE sessions (id TEXT PRIMARY KEY, services TEXT NOT NULL);
		INSERT INTO hosts VALUES ('h1');
		INSERT INTO sessions VALUES ('s_1', '[]');
		PRAGMA user_version = 5;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path, discard())
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := s.Hosts()
	if err != nil || len(hosts) != 0 {
		t.Fatalf("hosts after a wipe = %v, %v", hosts, err)
	}
	boxes, err := s.Sandboxes("")
	if err != nil || len(boxes) != 0 {
		t.Fatalf("sandboxes after a wipe = %v, %v", boxes, err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}

	// Reopening at the same version keeps what is there.
	if err := s.InsertPendingHost(host("h2", "fp2")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path, discard())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	hosts, err = again.Hosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != "h2" {
		t.Errorf("hosts after reopening = %v, want just h2", hosts)
	}
}

func TestHostLifecycle(t *testing.T) {
	s := open(t)
	if err := s.InsertPendingHost(host("h1", "fp1")); err != nil {
		t.Fatal(err)
	}

	h, found, err := s.HostByFingerprint("fp1")
	if err != nil || !found {
		t.Fatalf("by fingerprint = %v, %v", found, err)
	}
	if h.Status != HostPending || h.ApproveCode != "AAAA-AA" {
		t.Errorf("new host = %+v", h)
	}
	if !slices.Equal(h.Tags, []string{"arch:amd64", "os:linux"}) {
		t.Errorf("tags = %v", h.Tags)
	}
	if h.LastSeenAt == nil {
		t.Error("a new host has been seen, by definition")
	}

	// A hello refreshes what the worker reports and leaves the rest alone.
	name, max := "renamed", 9
	err = s.TouchHost("h1", HostPatch{Name: &name, MaxSandboxes: &max, Tags: []string{"gpu"}})
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ = s.Host("h1")
	if h.Name != "renamed" || h.MaxSandboxes != 9 || !slices.Equal(h.Tags, []string{"gpu"}) {
		t.Errorf("after touch = %+v", h)
	}
	// A heartbeat names only the capacity, and must not blank the rest.
	max = 4
	if err := s.TouchHost("h1", HostPatch{MaxSandboxes: &max}); err != nil {
		t.Fatal(err)
	}
	h, _, _ = s.Host("h1")
	if h.Name != "renamed" || h.MaxSandboxes != 4 || !slices.Equal(h.Tags, []string{"gpu"}) {
		t.Errorf("after a heartbeat = %+v", h)
	}

	if err := s.ApproveHost("h1"); err != nil {
		t.Fatal(err)
	}
	h, _, _ = s.Host("h1")
	if h.Status != HostApproved || h.ApproveCode != "" {
		t.Errorf("approved = %+v, want no code left", h)
	}
	if err := s.RevokeHost("h1"); err != nil {
		t.Fatal(err)
	}
	h, _, _ = s.Host("h1")
	if h.Status != HostRevoked {
		t.Errorf("revoked = %+v", h)
	}

	if _, found, err := s.Host("nope"); found || err != nil {
		t.Errorf("a missing host is not an error: %v, %v", found, err)
	}
}

func TestSandboxRoundTrip(t *testing.T) {
	s := open(t)
	in := Sandbox{
		ID: "s_1", OwnerID: "me", Image: "img:1",
		Cmd: []string{"bash", "-l"}, Env: map[string]string{"A": "1"},
		Tags: []string{"driver:docker"}, IdleTimeoutS: 60, CreatedAt: 10,
	}
	if err := s.InsertPendingHost(host("h1", "fp1")); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSandbox(in); err != nil {
		t.Fatal(err)
	}
	// Null cmd is the image's own entry, and must stay distinct from an empty list.
	err := s.InsertSandbox(Sandbox{ID: "s_2", OwnerID: "me", Image: "i", IdleTimeoutS: 60, CreatedAt: 11})
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := s.Sandbox("s_1")
	if err != nil || !found {
		t.Fatalf("get = %v, %v", found, err)
	}
	if got.Status != Queued || got.HostID != nil || got.EndedReason != "" {
		t.Errorf("new sandbox = %+v", got)
	}
	if !slices.Equal(got.Cmd, in.Cmd) || got.Env["A"] != "1" || !slices.Equal(got.Tags, in.Tags) {
		t.Errorf("json columns = %+v", got)
	}
	plain, _, _ := s.Sandbox("s_2")
	if plain.Cmd != nil {
		t.Errorf("cmd = %v, want nil for the image's default entry", plain.Cmd)
	}
	if plain.Env == nil || plain.Tags == nil {
		t.Errorf("env and tags read back as empty, never nil: %+v", plain)
	}

	// Newest first, and scoped to an owner when asked.
	all, err := s.Sandboxes("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "s_2" {
		t.Errorf("list = %v, want newest first", ids(all))
	}
	if mine, _ := s.Sandboxes("someone-else"); len(mine) != 0 {
		t.Errorf("another owner sees %v", ids(mine))
	}

	if err := s.MarkCreating("s_1", "h1"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Sandbox("s_1")
	if got.Status != Creating || got.HostID == nil || *got.HostID != "h1" {
		t.Errorf("creating = %+v", got)
	}
	if err := s.MarkRunning("s_1"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Sandbox("s_1")
	if got.Status != Running || got.StartedAt == nil {
		t.Errorf("running = %+v", got)
	}

	active, err := s.ActiveOnHost("h1")
	if err != nil || len(active) != 1 {
		t.Fatalf("active on host = %v, %v", ids(active), err)
	}
	queued, _ := s.Queued()
	if len(queued) != 1 || queued[0].ID != "s_2" {
		t.Errorf("queued = %v", ids(queued))
	}
}

func TestTheFirstEndingReasonWins(t *testing.T) {
	s := open(t)
	if err := s.InsertSandbox(Sandbox{ID: "s_1", OwnerID: "me", Image: "i", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnded("s_1", wire.EndIdle, "no traffic"); err != nil {
		t.Fatal(err)
	}
	// A sweep racing a DELETE must not rewrite why the sandbox stopped.
	if err := s.MarkEnded("s_1", wire.EndClosed, ""); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Sandbox("s_1")
	if got.EndedReason != wire.EndIdle || got.EndedDetail != "no traffic" || got.EndedAt == nil {
		t.Errorf("ended = %+v", got)
	}
}

func TestUnknownSinceIsSetOnBootAndClearedByTheHost(t *testing.T) {
	s := open(t)
	if err := s.InsertPendingHost(host("h1", "fp1")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s_1", "s_2"} {
		if err := s.InsertSandbox(Sandbox{ID: id, OwnerID: "me", Image: "i", CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkCreating(id, "h1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkActiveUnknown(); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Sandbox("s_1")
	if got.UnknownSince == nil {
		t.Fatal("an active sandbox is doubted after a restart")
	}
	if err := s.MarkKnown("s_1"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Sandbox("s_1")
	if got.UnknownSince != nil {
		t.Error("the host reported it; the doubt is gone")
	}
	other, _, _ := s.Sandbox("s_2")
	if other.UnknownSince == nil {
		t.Error("only the reported one is cleared")
	}
}

func TestDumpIsFixedToOurTables(t *testing.T) {
	s := open(t)
	if err := s.InsertPendingHost(host("h1", "fp1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Dump("hosts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "fingerprint=fp1") {
		t.Errorf("dump = %q", got)
	}
	if _, err := s.Dump("sqlite_master"); err == nil {
		t.Error("Dump is not a query API")
	}
}

func host(id, fp string) Host {
	return Host{
		ID: id, Name: id, Fingerprint: fp, ApproveCode: "AAAA-AA",
		MaxSandboxes: 2, Tags: []string{"arch:amd64", "os:linux"},
	}
}

func ids(rows []Sandbox) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// A provider host is approved at every boot and keeps its id, so sandboxes placed on it
// before a restart still name a row that exists.
func TestProviderHostIsUpsertedApproved(t *testing.T) {
	s := open(t)
	first, err := s.UpsertProviderHost(Host{Name: "cp", Fingerprint: "fp-provider", MaxSandboxes: 2, Tags: []string{"provider:local"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != HostApproved || first.ID == "" || first.LastSeenAt == nil {
		t.Fatalf("first = %+v", first)
	}
	if err := s.RevokeHost(first.ID); err != nil {
		t.Fatal(err)
	}

	second, err := s.UpsertProviderHost(Host{Name: "cp2", Fingerprint: "fp-provider", MaxSandboxes: 3, Tags: []string{"provider:local", "gpu"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("the id changed across boots: %s then %s", first.ID, second.ID)
	}
	if second.Status != HostApproved || second.Name != "cp2" || second.MaxSandboxes != 3 || !slices.Equal(second.Tags, []string{"provider:local", "gpu"}) {
		t.Errorf("second = %+v, want re-approved with the new name, capacity and tags", second)
	}
	dump, err := s.Dump("hosts")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(dump, "\n") != 1 || !strings.Contains(dump, "status=approved") || strings.Contains(dump, "approve_code=") && !strings.Contains(dump, "approve_code= ") {
		t.Errorf("hosts table:\n%s", dump)
	}
}
