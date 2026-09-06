# Plan: a config file, and sandboxes from more than one kind of host

Temporary. This is the delta between the code and the shape below; a phase is deleted from
here when it lands, and the file goes when the last one does. `VISION.md` is the permanent
half. Decisions and their rejected alternatives will be ADRs under `docs/adr/`.

## Goal

Today a sandbox can run in exactly one place: a machine running the worker, which dialled
in. Adding a runtime is already a new driver behind the worker's interface; adding a
*place* is not possible without a daemon there. Operators configure both daemons through
flat `SANDBOXD_*` variables, which cannot describe a provider block or carry a comment.

After this plan:

- Each daemon reads one YAML file. Providers and drivers are blocks in it.
- The control plane can run sandboxes itself — on its own Docker engine, or in a
  Kubernetes namespace — with no daemon in between, and those show up as hosts like any
  other. Callers select with tags and cannot tell the difference.
- The worker stays as the universal path: machines behind NAT, laptops, VM drivers.

Two words, used precisely from here on:

- A **driver** is *what runs a sandbox*: `docker` today; `kubernetes`, `qemu` next. It owns
  create, exec-in-PTY, dial, destroy, list-by-label.
- A **provider** is *where the manager that owns the PTY runs*: on a remote machine behind
  the tunnel (`workers`), or inside the control plane process (`local`).

Every combination is valid. Docker in the control plane is the single-node and dev story;
Kubernetes in a worker is a worker pod with cluster credentials; QEMU is worker-only.

## Target shape

```
internal/wire/                 the wire contract; a leaf                           (exists)
internal/conf/                 the file loader: strict YAML, ${VAR}, _file twins   (phase 1)
internal/sandbox/              Driver, PTY, Manager, Fanout, Ring; imports wire only (phase 2)
internal/sandbox/docker/       moved from internal/worker/driver                   (phase 2)
internal/sandbox/kubernetes/   Pod, exec, portforward over net/http + websocket    (phase 4)
internal/worker/               config, tunnel, session, port: the remote face of a Manager
internal/cp/hosts/             the tunnel provider, unchanged
internal/cp/fleet/             routes host id → hub or provider, behind cp's interfaces (phase 3)
internal/cp/local/             a sandbox.Manager in-process, presented as one host  (phase 3)
```

Import rules, enforced by `depguard`: `sandbox` imports `wire` only and both daemons import
it; `conf` is a leaf; `cp` and `worker` still never import each other; `cp` still imports
none of its subpackages; `fleet` is denied `hosts` and `local`, which register into it.
`sandbox` is shared code, not a channel between the daemons — they still meet on the wire.

Invariants that do not move: the scheduler is the only writer of sandbox state; the API
does not change (no `provider` field — tags select); secrets never reach a row or a
persisted object; one writer goroutine per socket.

## The config file (phase 1)

One YAML file per daemon, found by `--config`, then `SANDBOXD_CONFIG`, then
`/etc/sandboxd/{api,worker}.yaml`. Traefik's shape: `providers:` keyed by type, one block
each, presence enables; `driver:` on the worker, exactly one.

```yaml
# /etc/sandboxd/api.yaml
listen: ":8080"
public_url: https://api.example.com
preview_domain: preview.example.com
db: /var/lib/sandboxd-api/cp.db

auth:
  service_token: ${SANDBOXD_SERVICE_TOKEN}       # or service_token_file:
  # secret / secret_file: signs capability tokens; defaults to the service token

sandbox_env:                                    # under every sandbox, never persisted
  LLM_BASE_URL: ${LLM_BASE_URL:-https://gateway.example.com}
sandbox_env_files:
  LLM_API_KEY: /run/secrets/llm-key

providers:                                      # absent = `workers: {}`; present = exactly this
  workers:
    join_token_file: /run/secrets/sandboxd-join-token
  local:                                        # phase 3
    docker_sock: /var/run/docker.sock
    entry: /usr/local/bin/sandboxd-entry
    max_sandboxes: 4
    tags: [arch:amd64]
    name: cp
  kubernetes:                                   # phase 4
    kubeconfig: ""                              # empty = in-cluster
    namespace: sandboxes
    max_sandboxes: 20
    tags: [arch:amd64]
    name: k8s
    pod:
      runtime_class: gvisor
      node_selector: {pool: sandboxes}
      service_account: ""
      image_pull_policy: Always
      create_timeout_s: 300
      resources: {limits: {cpu: "2", memory: 4Gi}}
```

```yaml
# /etc/sandboxd/worker.yaml
url: wss://api.example.com
name: ${HOSTNAME}
max_sandboxes: 4
tags: [gpu:a100]
join_token: ${SANDBOXD_JOIN_TOKEN:?set it in the unit}
identity: /var/lib/sandboxd-worker/host.json   # state, not config; stays a separate file

driver:                                         # exactly one block
  docker:
    sock: /var/run/docker.sock
    entry: /usr/local/bin/sandboxd-entry
  # kubernetes: {kubeconfig, namespace, pod}    # phase 4
```

Rules:

- **Strict.** An unknown key refuses to boot, with line and column.
- **One source.** With a file, the environment reaches the configuration only by
  substitution in string values: `${VAR}`, `${VAR:-default}`, `${VAR:?message}`, `$$` for a
  literal. Unset with no default is a boot error, never an empty string. Keys are not
  expanded; a bare `$HOME` passes through. Substitution runs on the parsed node tree, so a
  value that becomes a number still decodes as one, and errors still name a line.
- **Secrets by file too.** Every secret key has a `_file` twin; both set is an error.
- **No file, old behaviour.** Today's `SANDBOXD_*` variables apply unchanged when no file
  is found. That keeps deployed units and `scripts/dev` booting; it is compatibility, not
  design. With a file, those variables are ignored and `--check-config` says so.
- `--check-config` loads, validates, prints the source used and the effective config with
  secrets redacted, exits 0 or 1.
- `SANDBOXD_WORKER_CONFIG` names the identity file today; it becomes
  `SANDBOXD_WORKER_IDENTITY`, old name warned about for one release.
- The YAML module is `github.com/go-faster/yaml`: already in the graph through ogen,
  promoted to direct; `KnownFields(true)` is the strict mode and its errors carry positions.

**Touches:** `internal/conf` (new; `Load[T any](path, env) (T, []error)` — generic so no
`any` in the signature); `cp/config.go` and `worker/config.go` (yaml tags, `Validate`, the
legacy path only when no file); both `cmd`s (`--config`, `--check-config`);
`infra/aws`, `infra/proxmox` (units read the yaml; env files shrink to nothing); `.golangci.yml`;
`AGENTS.md`. The `local` and `kubernetes` blocks decode and are refused as "not built yet"
until their phases land.

**Done when:** the four substitution forms, unset-is-an-error, `$$`, strict decode with a
line number, `_file` twins, three faults reported in one call, every existing env-only
test still passing with no file, a redacted print that never contains a secret.

## Extract `internal/sandbox` (phase 2)

The PTY, ring, fanout and idle reaper live in `internal/worker`, interleaved with the
tunnel. A provider inside the control plane needs exactly those and none of the tunnel,
and `cp` may not import `worker`. Without this phase, phase 3 is a copy of `manager.go`.

Pure move, no behaviour change: `manager.go`, `fanout.go`, `ring.go` and `Viewer` go to
`internal/sandbox`; `internal/worker/driver` becomes `internal/sandbox/docker`. `Dial`
joins the `Driver` interface (a provider in the control plane has no tunnel to declare a
separate dialer on; every driver has one), so the tunnel's `Dialer` goes.

```go
type Driver interface {
    Create(ctx, sid, image string) (id string, err error)     // entrypoint held idle
    Attach(ctx, id string, cmd []string, env map[string]string, size wire.Size) (PTY, error)
    Dial(ctx, id string, port int) (net.Conn, error)          // for the preview proxy
    Destroy(ctx, id string) error
    ListManaged(ctx) ([]Managed, error)                       // by label, for the orphan sweep
}
```

**Touches:** `git mv`, import paths, the tunnel's constructor, `cmd/sandboxd-worker`,
`.golangci.yml`, `AGENTS.md`. Not `runtime` (stdlib name) or `host` (`cp/hosts` exists).

**Done when:** every existing manager, ring, tunnel and docker test passes unchanged;
`SANDBOXD_DOCKER_TEST=1 go test ./internal/sandbox/docker/` is the live check.

## The provider seam and `local` (phase 3)

The scheduler, attach bridge, preview proxy and admin host list each declare a small
interface (`placement`, `Opener`, `dialer`, `presence`) and `hosts.Hub` is the only
implementation. Nothing else can be plugged in without editing the scheduler.

- `cp/fleet` implements those interfaces verbatim by delegating on host id: the hub for
  ids it enrolled, a provider for ids it created. `cmd/sandboxd-api` builds it and hands it
  to the scheduler, bridge, proxy and service where it hands the hub today.
- `cp/local` wraps one `sandbox.Manager` over one driver (Docker here) and presents it as
  one host: an approved row in `hosts` upserted at every boot, fingerprint
  `sha256("sandboxd/provider/local")`, tags from its block plus `provider:local`,
  `driver:docker`, `arch:`, `os:`. Placement, the queue, the 422 for unsatisfiable tags and
  `GET /hosts` work unmodified because they already read rows and ask the fleet.
- It reports like a worker would — `HostOnline{running: []}` at boot, a heartbeat on the
  same interval, started and ended as the manager emits them — so reconciliation covers a
  restart with no new path.
- `OpenPTY` returns a PTY whose `Read` yields the ring replay then live bytes; `Write` and
  `Resize` go to the manager; `Dial` is the driver's. Full-queue policy unchanged: a viewer
  is dropped, a port stream blocks.

**The trade, written down:** a sandbox on a CP-hosted provider does not survive a CP
restart. Its PTY was an exec held by this process; at boot the provider reports nothing
running, the row reconciles to `lost`, the orphan sweep removes the container. The worker
path keeps that property; choosing a provider gives it up for one fewer daemon.

**Touches:** `cp/fleet`, `cp/local` (new); `store.UpsertProviderHost` (one method, one
`store.Dump` test); the hub registers and forgets ids in the fleet (two calls);
`cp/config.go` (`providers.local`); `cmd/sandboxd-api`; `.golangci.yml`; `AGENTS.md`.
`revoke` on a provider host holds until the next boot re-approves it.

**Done when:** fleet routes and never panics on an unknown id; `local` against the fake
driver emits started/ended, replays then streams, dials, counts capacity; the CP suite on
`:memory:` places `tags: [provider:local]` on the provider and `[provider:workers]` on a
fake tunnel, most-free-slots across both with neither, and reconciles a stale `creating`
row on the provider to `lost`; a live `SANDBOXD_DOCKER_TEST=1` end-to-end with only
`providers.local` configured.

## The Kubernetes driver (phase 4)

The one runtime where the API server already proxies both halves of the contract:
`pods/exec` with a TTY and `pods/portforward`. One Pod per sandbox, `restartPolicy: Never`,
one container with `command: ["sleep", "infinity"]` (the same idle trick as Docker), then
exec over the `v5.channel.k8s.io` websocket subprotocol for the PTY and `portforward.k8s.io`
for `Dial`. `ListManaged` is a label selector (`sandboxd.io/sid`, `sandboxd.io/owner`). Env
travels in the exec request, so no secret lands in an object the API server persists.
Credentials from the in-cluster service account files or a kubeconfig (`clusters`, `users`,
`contexts` with token or client cert; anything else is an error naming the field). Runs in
a worker (`driver.kubernetes`) or in the control plane (`providers.kubernetes` = `local`
over this driver; no new provider package). Not `client-go`: thirty modules for one JSON
POST and two websocket subprotocols, against a closed dependency list.

A Pod that does not reach `Running` within `pod.create_timeout_s` ends `failed`; the exec
stream closing ends `exited`; `Destroy` deletes with `gracePeriodSeconds: 0`.

**Touches:** `sandbox/kubernetes` (`client.go`, `pod.go`, `exec.go`, `portforward.go`,
`kubeconfig.go`); `worker/config.go` (`driver.kubernetes`); `cp/config.go`,
`cmd/sandboxd-api`; RBAC in the deploy notes (`pods` create/get/list/delete/watch,
`pods/exec`, `pods/portforward`, one namespace).

**Done when:** exec and portforward framing pass against an `httptest.Server` speaking the
subprotocols; kubeconfig parsing handles token, client cert, and rejects `exec:`; the
manager suite runs against this driver behind a fake transport; a live
`SANDBOXD_K8S_TEST=1` run against `kind` creates, attaches, dials, destroys, sweeps clean.

## Later, in the order they earn their keep

1. **`local.host` and `local.tls`.** The moby client already speaks `tcp://` with client
   certs, so pointing `local` at a remote engine on a private network is configuration.
   One block per type stops being enough at two engines; a list under `local.engines` is
   the likely shape, with Swarm node discovery as an optional source of it. Decide then.
2. **`sandbox/qemu`.** Worker-only. A VM per sandbox, a PTY on the serial console or a
   vsock agent, `Dial` over a tap. How an image becomes a disk is the spec's main question.
3. **`sandbox/podman`.** Likely the Docker driver over a different socket; verify first.

## Not doing

- **A Swarm provider.** Swarm has no `service exec`; a PTY on a task means reaching the
  owning node's engine, which Swarm does not proxy. Swarm deploys workers well (a
  global-mode service with the socket mounted); it does not replace them.
- **An ECS provider.** Fargate has no socket; ECS Exec is SSM's session protocol. Workers
  on EC2 cover it.
- **A `provider` field in the API**, or any API change at all.
- **Env overriding the file per scalar.** Two ways to set one value; `${VAR}` already lets
  the file say which values come from outside.

## Order

| Phase | Depends on | New packages                    | `go.mod`                  |
| ----- | ---------- | ------------------------------- | ------------------------- |
| 1     | —          | `internal/conf`                 | `go-faster/yaml` → direct |
| 2     | —          | `internal/sandbox`, `/docker`   | —                         |
| 3     | 1, 2       | `cp/fleet`, `cp/local`          | —                         |
| 4     | 2, 3       | `sandbox/kubernetes`            | —                         |

Phases 1 and 2 are independent and can be two branches. Phase 2 is the largest diff and the
smallest risk. Phase 3 is where the design changes and the first phase an operator can see.
Every phase leaves the gate green:
`golangci-lint fmt && go vet ./... && golangci-lint run && go test ./...`.
