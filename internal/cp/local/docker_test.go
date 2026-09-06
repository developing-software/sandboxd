package local_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/local"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/sandbox/docker"
	"sandboxd/internal/wire"
)

// The one live check: a control plane with only `providers.docker`, end to end against a
// real Engine — placed by the real scheduler, attached, ended. It needs Docker, so
// `go test ./...` on a machine without it still passes.
//
//	SANDBOXD_DOCKER_TEST=1 go test ./internal/cp/local/
func TestLiveDockerProvider(t *testing.T) {
	if os.Getenv("SANDBOXD_DOCKER_TEST") == "" {
		t.Skip("set SANDBOXD_DOCKER_TEST=1 to run against a real Docker Engine")
	}
	cfg := docker.Config{Sock: os.Getenv("DOCKER_SOCK")}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// A distinct owner, so this never touches a real control plane's containers.
	owner := "go-test-" + wire.RandomID(4)
	drv, err := docker.New(cfg, owner, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = drv.Close() })

	st, err := store.Open(":memory:", discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	events := make(chan cp.Event, 32)
	prov, err := local.New(ctx, drv, local.Options{Name: "cp", Driver: owner, Entry: []string{"sh"}, Max: 2}, st, events, discard())
	if err != nil {
		t.Fatal(err)
	}
	sched := cp.NewScheduler(st, prov, cp.MostFreeSlots, nil, events, discard())
	if err := sched.Boot(); err != nil {
		t.Fatal(err)
	}
	go sched.Run(ctx)
	go prov.Run(ctx)
	t.Cleanup(func() { prov.Close(context.Background()) })

	sb := store.Sandbox{
		ID: wire.NewSandboxID(), OwnerID: "o", Image: "alpine:3",
		Cmd: []string{"sh", "-c", "echo hello-from-$GREETER; sleep 60"},
		Env: map[string]string{}, IdleTimeoutS: 60, CreatedAt: time.Now().UnixMilli(),
	}
	if err := sched.Submit(ctx, sb, map[string]string{"GREETER": "provider"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute) // a cold pull is the slow part
	for status(t, st, sb.ID) != store.Running {
		if time.Now().After(deadline) {
			row, _, _ := st.Sandbox(sb.ID)
			t.Fatalf("never ran: %+v", row)
		}
		time.Sleep(50 * time.Millisecond)
	}

	term, err := prov.OpenPTY(prov.ID(), sb.ID, wire.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, err := term.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); !contains(got, "hello-from-provider") {
		// The line may arrive in two reads; take one more before giving up.
		m, _ := term.Read(buf[n:])
		if got = string(buf[:n+m]); !contains(got, "hello-from-provider") {
			t.Errorf("terminal said %q", got)
		}
	}
	_ = term.Close()

	if err := sched.Cancel(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return status(t, st, sb.ID) == store.Ended })
	managed, err := drv.ListManaged(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 0 {
		t.Errorf("containers left behind: %v", managed)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
