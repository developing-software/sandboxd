package driver

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"sandboxd/internal/wire"
)

// The one test that talks to a real Engine. It is the phase 2 checklist's driver half —
// create, exec a PTY, dial a port inside the container, destroy, sweep by label — and it
// needs Docker, so `go test ./...` on a machine without it still passes.
//
//	SANDBOXD_DOCKER_TEST=1 go test ./internal/worker/driver/
const testImage = "alpine:3"

func liveDocker(t *testing.T) *Docker {
	t.Helper()
	if os.Getenv("SANDBOXD_DOCKER_TEST") == "" {
		t.Skip("set SANDBOXD_DOCKER_TEST=1 to run against a real Docker Engine")
	}
	sock := os.Getenv("DOCKER_SOCK")
	if sock == "" {
		sock = "/var/run/docker.sock"
	}
	// A distinct owner, so this never touches a real worker's containers on the machine.
	d, err := NewDocker(sock, "go-test-"+wire.RandomID(4), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// Which references cost a registry round trip on every create, and which are read straight
// from the cache. No Engine needed: this is the whole refresh decision.
func TestMovesUnderUs(t *testing.T) {
	for img, want := range map[string]bool{
		"ghcr.io/developing-software/sandboxd-agent:latest": true,
		"ghcr.io/developing-software/sandboxd-agent":        true,
		"alpine":             true,
		"localhost:5000/app": true, // a port is not a tag
		"ghcr.io/developing-software/sandboxd-agent:sha-abc": false,
		"ubuntu:24.04":          false,
		"localhost:5000/app:v1": false,
		"ghcr.io/developing-software/sandboxd-agent@sha256:0000000000000000000000000000000000000000000000000000000000000000": false,
		"alpine:latest@sha256:0000000000000000000000000000000000000000000000000000000000000000":                              false,
	} {
		if got := movesUnderUs(img); got != want {
			t.Errorf("movesUnderUs(%q) = %v, want %v", img, got, want)
		}
	}
}

func TestDockerRunsASandbox(t *testing.T) {
	d := liveDocker(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	sid := wire.NewSandboxID()
	id, err := d.Create(ctx, sid, testImage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Destroy(context.WithoutCancel(ctx), id) })

	pty, err := d.Attach(ctx, id, []string{"/bin/sh"}, map[string]string{
		"SANDBOXD_SESSION_ID": sid,
		"TERM":                "xterm-256color",
	}, wire.Size{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()

	// The env reached the exec, and the terminal is a terminal.
	if _, err := io.WriteString(pty, "echo $SANDBOXD_SESSION_ID; tty\n"); err != nil {
		t.Fatal(err)
	}
	// `tty` answering with a pts device is the proof it is a terminal and not a pipe.
	out := readUntil(t, pty, "/dev/pts/")
	if !strings.Contains(out, sid) {
		t.Errorf("the exec's env should carry the sandbox id, got:\n%s", out)
	}

	if err := pty.Resize(ctx, wire.Size{Cols: 80, Rows: 24}); err != nil {
		t.Errorf("resize: %v", err)
	}

	// A port inside the container, reached the way the preview proxy reaches one.
	if _, err := io.WriteString(pty, "nc -lk -p 8000 -e /bin/cat &\n"); err != nil {
		t.Fatal(err)
	}
	conn := dialWithRetry(t, ctx, d, id, 8000)
	defer conn.Close()
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 5)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(echo) != "ping\n" {
		t.Errorf("echoed %q", echo)
	}

	// Found by label, which is what the orphan sweep depends on.
	managed, err := d.ListManaged(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(managed, func(m Managed) bool { return m.ID == id && m.SID == sid }) {
		t.Errorf("ListManaged = %v, want the container we created", managed)
	}

	if err := d.Destroy(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Destroying twice is what a sweep racing an end does, and it is not an error.
	if err := d.Destroy(ctx, id); err != nil {
		t.Errorf("second destroy: %v", err)
	}
	managed, err = d.ListManaged(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 0 {
		t.Errorf("ListManaged after destroy = %v", managed)
	}
}

func readUntil(t *testing.T, r io.Reader, want string) string {
	t.Helper()
	var seen bytes.Buffer
	buf := make([]byte, 4096)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n, err := r.Read(buf)
		seen.Write(buf[:n])
		if bytes.Contains(seen.Bytes(), []byte(want)) {
			return seen.String()
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("never saw %q in:\n%s", want, seen.String())
	return ""
}

func dialWithRetry(t *testing.T, ctx context.Context, d *Docker, id string, port int) net.Conn {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		conn, err := d.Dial(ctx, id, port)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %d: %v", port, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
