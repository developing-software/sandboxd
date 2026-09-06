// Package docker runs sandboxes as containers on a Docker Engine, over the moby client.
package docker

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"sandboxd/internal/sandbox"
	"sandboxd/internal/wire"
)

// Every container carries these. Anything created without them is invisible to the orphan
// sweep, which is how a sandbox from a previous run gets reclaimed.
const (
	labelManaged = "sandboxd.managed"
	labelSID     = "sandboxd.sid"
	labelHost    = "sandboxd.host"
)

// Driver runs sandboxes as containers on the local Engine.
type Driver struct {
	cli *client.Client
	// owner scopes create, list and sweep to this worker's identity, so several workers
	// sharing one Engine never reclaim each other's containers.
	owner string
	log   *slog.Logger
}

// New connects to the Engine on cfg.Sock. owner scopes create, list and sweep to one
// identity — a worker's fingerprint, or a provider's — so several sharing one Engine
// never reclaim each other's containers.
func New(cfg Config, owner string, log *slog.Logger) (*Driver, error) {
	sock := cfg.Sock
	// Version negotiation is on by default in this client, so an older Engine on a host
	// still works without asking for it.
	cli, err := client.New(client.WithHost("unix://" + sock))
	if err != nil {
		return nil, fmt.Errorf("docker: connect %s: %w", sock, err)
	}
	return &Driver{cli: cli, owner: owner, log: log}, nil
}

func (d *Driver) Close() error { return d.cli.Close() }

// Create starts a container kept idle on `sleep infinity`, so a PTY can be exec'd into it
// later. No env is set here: the sandbox's own env, secrets included, arrives at exec.
func (d *Driver) Create(ctx context.Context, sid, img string) (string, error) {
	init := true
	opts := client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  img,
			Cmd:    []string{"sleep", "infinity"},
			Labels: map[string]string{labelManaged: "1", labelSID: sid, labelHost: d.owner},
		},
		HostConfig: &container.HostConfig{
			Init: &init,
			// Lets a sandbox reach services on the host, e.g. an LLM gateway on localhost.
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
		},
		Name: "sandboxd-" + sid,
	}

	d.refresh(ctx, img)

	res, err := d.cli.ContainerCreate(ctx, opts)
	if cerrdefs.IsNotFound(err) {
		if err := d.pull(ctx, img); err != nil {
			return "", err
		}
		res, err = d.cli.ContainerCreate(ctx, opts)
	}
	if err != nil {
		return "", fmt.Errorf("docker: create %s: %w", sid, err)
	}

	if _, err := d.cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		// The container exists but will never run; do not leave it behind.
		_ = d.Destroy(ctx, res.ID)
		return "", fmt.Errorf("docker: start %s: %w", sid, err)
	}
	return res.ID, nil
}

// refresh re-pulls a reference that someone else can move under us. Create below misses
// only when nothing is cached at all, so without this a host runs the copy it pulled first
// for as long as it lives — a republished `:latest` reaching nobody.
func (d *Driver) refresh(ctx context.Context, img string) {
	if !movesUnderUs(img) || d.builtHere(ctx, img) {
		return
	}
	// Not fatal on its own: a cached copy still runs, and where there is none the create
	// below misses and its own pull reports the real failure.
	if err := d.pull(ctx, img); err != nil {
		d.log.Warn("image refresh failed, running whatever is cached", "image", img, "err", err)
	}
}

// Which references get refreshed. This is Kubernetes' imagePullPolicy default and for its
// reason: `latest` is the tag everyone republishes, while re-pulling every tag would cost
// `ubuntu:24.04` a registry round trip — and a Docker Hub rate limit — per sandbox.
func movesUnderUs(img string) bool {
	if strings.Contains(img, "@") {
		return false // pinned to a digest, which cannot mean anything else later
	}
	// Only the last path element can carry the tag: a registry host may have a port, as
	// in localhost:5000/app.
	_, tag, tagged := strings.Cut(img[strings.LastIndex(img, "/")+1:], ":")
	return !tagged || tag == "" || tag == "latest"
}

// builtHere reports an image this Engine built and never got from a registry, which has no
// repo digest. images/README.md documents building under the published tag to try an entry
// script without pushing one, and a refresh would silently pull that work away.
func (d *Driver) builtHere(ctx context.Context, img string) bool {
	res, err := d.cli.ImageInspect(ctx, img)
	if err != nil {
		return false // absent, most likely, and the pull is needed either way
	}
	return len(res.RepoDigests) == 0
}

func (d *Driver) pull(ctx context.Context, img string) error {
	body, err := d.cli.ImagePull(ctx, img, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull %s: %w", img, err)
	}
	defer func() { _ = body.Close() }()
	// Wait drains the progress stream and reports what the daemon reported, rather than
	// discarding it and failing later on a create that still has no image.
	if err := body.Wait(ctx); err != nil {
		return fmt.Errorf("docker: pull %s: %w", img, err)
	}
	return nil
}

// Attach execs cmd in a PTY inside the container. With a TTY the hijacked connection is
// the raw terminal in both directions — no stdcopy framing to undo.
func (d *Driver) Attach(
	ctx context.Context,
	id string,
	cmd []string,
	env map[string]string,
	size wire.Size,
) (sandbox.PTY, error) {
	exec, err := d.cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		TTY:          true,
		Cmd:          cmd,
		Env:          envList(env),
	})
	if err != nil {
		return nil, fmt.Errorf("docker: exec create: %w", err)
	}

	attached, err := d.cli.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{
		TTY:         true,
		ConsoleSize: consoleSize(size),
	})
	if err != nil {
		return nil, fmt.Errorf("docker: exec attach: %w", err)
	}
	return &dockerPTY{cli: d.cli, execID: exec.ID, hj: attached.HijackedResponse}, nil
}

// Dial opens a TCP connection to a port inside the container, for the preview proxy.
func (d *Driver) Dial(ctx context.Context, id string, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("docker: port %d is out of range", port)
	}
	res, err := d.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: inspect: %w", err)
	}
	ip := containerIP(res.Container)
	if !ip.IsValid() {
		return nil, fmt.Errorf("docker: container %s has no IP address", short(id))
	}

	var dialer net.Dialer
	addr := netip.AddrPortFrom(ip, uint16(port))
	conn, err := dialer.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return nil, fmt.Errorf("docker: dial %s: %w", addr, err)
	}
	return conn, nil
}

func (d *Driver) Destroy(ctx context.Context, id string) error {
	_, err := d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	// Gone, or already going: both mean the same thing to a caller that wants it gone,
	// and a sweep racing an end hits exactly this.
	if err == nil || cerrdefs.IsNotFound(err) || cerrdefs.IsConflict(err) {
		return nil
	}
	return fmt.Errorf("docker: remove: %w", err)
}

// ListManaged is the orphan sweep's input: every container this worker identity created,
// running or not.
func (d *Driver) ListManaged(ctx context.Context) ([]sandbox.Managed, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		Filters: client.Filters{}.
			Add("label", labelManaged+"=1").
			Add("label", labelHost+"="+d.owner),
	})
	if err != nil {
		return nil, fmt.Errorf("docker: list: %w", err)
	}
	out := make([]sandbox.Managed, 0, len(res.Items))
	for _, c := range res.Items {
		out = append(out, sandbox.Managed{ID: c.ID, SID: c.Labels[labelSID]})
	}
	return out, nil
}

type dockerPTY struct {
	cli    *client.Client
	execID string
	hj     client.HijackedResponse
}

func (p *dockerPTY) Read(b []byte) (int, error)  { return p.hj.Reader.Read(b) }
func (p *dockerPTY) Write(b []byte) (int, error) { return p.hj.Conn.Write(b) }

func (p *dockerPTY) Close() error {
	p.hj.Close()
	return nil
}

func (p *dockerPTY) Resize(ctx context.Context, size wire.Size) error {
	_, err := p.cli.ExecResize(ctx, p.execID, client.ExecResizeOptions(consoleSize(size)))
	if err != nil {
		return fmt.Errorf("docker: exec resize: %w", err)
	}
	return nil
}

func consoleSize(size wire.Size) client.ConsoleSize {
	return client.ConsoleSize{Height: uint(size.Rows), Width: uint(size.Cols)}
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	// Driver takes a list; sorting keeps a container's config reproducible.
	slices.Sort(out)
	return out
}

// containerIP picks by sorted network name, so a container attached to two networks
// always resolves to the same address rather than to whichever one map iteration found.
func containerIP(info container.InspectResponse) netip.Addr {
	if info.NetworkSettings == nil {
		return netip.Addr{}
	}
	for _, name := range slices.Sorted(maps.Keys(info.NetworkSettings.Networks)) {
		if net := info.NetworkSettings.Networks[name]; net != nil && net.IPAddress.IsValid() {
			return net.IPAddress
		}
	}
	return netip.Addr{}
}

func short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
