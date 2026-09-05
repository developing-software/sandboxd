# sandboxd — prototype design

Status: **signed 2026-09-01**; decisions 16 and 18 revised 2026-09-04 for the Go port, and
15, 16, 17 and 18 again 2026-09-05 at cutover, when the TypeScript daemons were deleted. This
is the v1 contract. Change it here first. `SPEC.md` says what the API is; `PLAN.md` says how
the port happens.

## What it is

A microservice that lets a parent application run **agent tasks** on a fleet of
self-hosted machines and supervise them through a browser terminal.

- A **host** is a real machine you own, running the `sandboxd` worker. It dials **out** to
  the control plane over one persistent WebSocket; nothing inbound is required.
- A **sandbox** is a fresh Docker container from an image, one command started in a PTY
  with some env, gone when it ends. Disposable. It was called a "session" until
  2026-09-04; see decision 5.
  The core does not know what runs inside. **Presets** turn a
  friendly request (repo + prompt + agent) into image/cmd/env. A preset is a folder
  of data, `presets/<name>/` (`preset.yaml` + `Dockerfile` + `entry.sh`), with its own
  image: `coding-agent` is the reason this service exists, `vscode` opens a repo in
  VS Code behind the preview proxy, `jupyter` does the same for a notebook server,
  and any image that honours the sandbox contract below is a valid use case.
- The control plane (**CP**) is the only API. It schedules sandboxes onto hosts,
  relays terminal bytes, proxies preview ports, and stores metadata. It is a
  single instance backed by SQLite and has no users of its own.

## Decisions (with the alternative that was rejected)

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Rejected                                                                                                                                                                                                                                                                                                                                                     |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | Worker dials out; one wss per host, multiplexed                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | CP dials in via SSH (dead behind NAT, no free preview URLs)                                                                                                                                                                                                                                                                                                  |
| 2   | Hosts are real machines; sandbox lib is a host-local driver                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | "Hosts" as sandbox-provider pointers (no PTY, vendor cost)                                                                                                                                                                                                                                                                                                   |
| 3   | One channel: raw PTY bytes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Second structured agent-event channel (2× protocol, harness coupling)                                                                                                                                                                                                                                                                                        |
| 4   | Daemon owns PTY + 256 KB ring buffer; CP stores no bytes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | tmux (TUI-in-TUI); CP-owned buffer (secrets at rest)                                                                                                                                                                                                                                                                                                         |
| 5   | One task = one sandbox. Named **sandbox** everywhere (revised 2026-09-04): the routes (`/sandboxes`), the model, the wire messages (`sandbox.create/destroy/started/ended`) and the table. Two names keep the old word because changing them breaks something outside this repo: `sid` and the `s_` id prefix on the wire, and `SANDBOXD_SESSION_ID` inside the container, which every preset's `entry.sh` reads                                                                                                                                | "Session" (the product is `sandboxd`; the resource is the thing it makes, and two nouns for one resource cost more than the rename); renaming `sid`/`SANDBOXD_SESSION_ID` too (breaks every built image for a word); Workspace with N terminals (not the product being built)                                                                              |
| 6   | CP trusts parent app: service token + `owner_id`; short-lived attach tokens for browsers                                                                                                                                                                                                                                                                                                                                                                                                                                                       | CP validates parent JWT; CP owns users                                                                                                                                                                                                                                                                                                                       |
| 7   | Secrets passed per-sandbox, forwarded, never persisted                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Central secret store; host-level secrets                                                                                                                                                                                                                                                                                                                     |
| 8   | CP auto-picks host by free slots; FIFO queue when full. **Host tags** (revised 2026-09-04): a host reports a flat set of strings in its `hello`, a sandbox may require some, and placement considers only hosts whose set contains all of them — most free slots still wins among those. Tags are opaque to the CP; `key:value` (`arch:amd64`, `driver:docker`, `virt:vm`) is a convention, not a schema. A worker auto-reports `arch:`, `os:` and `driver:`; the operator adds the rest with `SANDBOXD_WORKER_TAGS`. A requirement no approved host can ever satisfy is a 422 at create; one that current hosts are merely too full for queues as usual | Caller picks a host by id (couples the caller to the fleet, breaks when the host is drained); key/value labels with operators (`in`, `!=`, `exists`) — a selector language for a fleet of five machines; resource requests (CPU/GPU quantities) — that is real scheduling and stays out of v1; matching any tag rather than all (surprising: `arch:arm64` would place on x86) |
| 9   | Output = `git push` from inside the sandbox                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Patch export with read-only clone                                                                                                                                                                                                                                                                                                                            |
| 10  | Idle timeout: any PTY byte either direction resets; 30 m default                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Client-input-only; count preview traffic                                                                                                                                                                                                                                                                                                                     |
| 11  | Preview: `<port>-<sid>.preview.<domain>` + signed cookie; WebSocket upgrades bridged (frames re-encoded at the CP)                                                                                                                                                                                                                                                                                                                                                                                                                             | Path-based (breaks absolute paths); unauthenticated; HTTP-only preview (added 2026-09-02: Jupyter kernels need it)                                                                                                                                                                                                                                           |
| 12  | Enrollment: daemon self-generates secret, CP sees fingerprint, pending + short code, admin approves. Revised 2026-09-03: an optional join token (`SANDBOXD_JOIN_TOKEN` on both sides) skips the code — the operator who provisions the worker already holds it. The fingerprint stays the host's identity; the token only decides who approves.                                                                                                                                                                                                | Static shared secret as identity; mTLS; unconditional auto-approve                                                                                                                                                                                                                                                                                           |
| 13  | SQLite only; daemon is source of truth for running state                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Redis (solves nothing without multi-instance)                                                                                                                                                                                                                                                                                                                |
| 14  | A driver behind the worker's `Driver` interface. One fleet may mix drivers (revised 2026-09-04): each worker runs one, reports it as a `driver:` tag, and callers pick with sandbox tags. The Docker one is the official client (`github.com/moby/moby/client`, revised 2026-09-05), not the raw Engine API: its `ExecAttach` hands back a hijacked `net.Conn`, which is what lets the port delete the hand-rolled HTTP upgrade                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | Sandcastle / TanStack sandbox (they want to own the agent run); the raw Engine API over `net/http` (the hijack is the pain, and it is what the TypeScript worker hand-rolled); `github.com/docker/docker/client` (the same code, but a pre-modules import path pinned at `+incompatible`)                                                                                                                                                                                                                                                                                               |
| 15  | Presets are data: `presets/<name>/preset.yaml` + `Dockerfile` + `entry.sh`, one image per preset (`sandboxd-<name>:latest`), `image:` override per sandbox (revised 2026-09-03); presets live in `examples/ui` and the API never sees one — it takes image, cmd, env (revised again 2026-09-03)                                                                                                                                                                                                                                                    | One image for every preset; presets as TypeScript modules; caller-supplied image always; per-host image                                                                                                                                                                                                                                                      |
| 19  | Agent is a per-sandbox choice (`claude` \| `codex` \| `opencode` \| `shell`); one LLM gateway tuple (`base_url`, `api_key`, `model`) is mapped by the entry script onto each harness's own config; defaults (agent, model) live in the image's entry and operators override them with `SANDBOXD_SANDBOX_ENV_*`                                                                                                                                                                                                                                 | Claude-only; per-agent config schemas in the API; per-preset env vars on the CP                                                                                                                                                                                                                                                                              |
| 16  | Go for the two daemons (revised 2026-09-04, see `PLAN.md`): `cmd/sandboxd-api` and `cmd/sandboxd-worker`, one static binary each, stdlib first. `net/http` + `httputil.ReverseProxy` own the sockets and the preview proxy, `database/sql` + a cgo-free SQLite driver own the state behind consumer-declared interfaces, huma renders the OpenAPI document from the handler types. Bun stays for the reference client only (`examples/ui`). Cut over 2026-09-05: the TypeScript control plane, worker and `packages/core` are deleted, and the repo root is a Go module with no `package.json`. The wire protocol, the HTTP API, the tokens and the SQLite schema did not change | Bun for everything (2026-09-03: `Bun.serve`, `bun:sqlite`, Hono — hand-rolled HTTP and WebSocket framing over tunnel streams, 100 MB JIT binaries, no Kubernetes client); Elysia; multi-runtime targets (Workers, Lambda): the CP is a stateful single instance; Rust (ceremony the size does not need); Go for the UI too (a YAML loader and one HTML page) |
| 17  | `examples/ui` (revised 2026-09-05): a Bun + Hono app that serves the xterm.js page at `/`, holds the service token, resolves presets, and forwards everything else to the API. The parent-app stand-in, and after the cutover a genuine external client — its own `package.json`, `tsconfig.json` and linter config, importing nothing from this repo.                                                                                                                                                                                                                                                                                                                                            | No UI; real Svelte frontend; a `/dev` page inside the control plane                                                                                                                                                                                                                                                                                          |
| 18  | One Go module `sandboxd` at the root (revised 2026-09-04): `cmd/sandboxd-api`, `cmd/sandboxd-worker`, `internal/{wire,cp,worker}`; `depguard` in `.golangci.yml` enforces cp → wire, worker → wire, never each other, `wire` a leaf, and one tree per `cmd`; the dependency list in `PLAN.md` is closed. The control-plane tree is `cp`, not `api`: the binary is the API, the tree is the whole daemon, and `internal/api/httpapi` stutters. `cp` never imports its own subpackages either: it declares the interfaces it needs and `cmd/sandboxd-api` supplies them, which is what keeps the tree acyclic without a wiring framework. Cut over 2026-09-05: `apps/api`, `apps/worker` and `packages/core` are gone, the root has no `package.json` or workspace, and `examples/ui` is a standalone Bun app with its own tooling | Bun workspaces for everything (2026-09-03: `apps/api`, `apps/ui`, `apps/worker`, `packages/core`, fallow as the boundary); one package, `src/{protocol,shared,cp,agent,dev}`; `internal/` alone as the boundary (it only blocks imports from outside the module, not between our own trees); `internal/api` for the control plane (two meanings for one word, and a stuttering HTTP package inside it) |
| 20  | Generic core (`image, cmd, env, secret_env`) + presets as a declarative field→env schema; the image is the plugin, its entry script owns the semantics. The generic half is the API, the preset half is the UI (revised 2026-09-03)                                                                                                                                                                                                                                                                                                            | Agent kinds baked into the protocol and daemon; driver plugins in the daemon; per-preset code on the CP                                                                                                                                                                                                                                                      |
| 21  | A sandbox is exactly one container, from one Dockerfile (revised 2026-09-04). No sidecars, no per-sandbox network, no readiness probes: none of the presets needed them, and the compose translator, the catalog and the pod lifecycle were the largest part of the code for no user. A repo that needs a database runs it inside the sandbox or points at one outside                                                                                                                                                                         | Sidecar `services` from a compose document plus a catalog (`services.yaml`), added 2026-09-03 and removed here; compose as the runtime (`docker compose up` per session); CP reading `.sandboxd.yaml` from the repo                                                                                                                                          |

## Topology

```
  host "hetzner-1"                     host "laptop" (NAT)
  ┌──────────────────────┐             ┌──────────────────────┐
  │ sandboxd worker      │             │ sandboxd worker      │
  │  ├ sandbox s_1 ─ pty │             │  └ sandbox s_3 ─ pty │
  │  └ sandbox s_2 ─ pty │             └──────────┬───────────┘
  └──────────┬───────────┘                        │
             │ wss (outbound, multiplexed)        │
             └────────────────┬───────────────────┘
                              ▼
                     ┌─────────────────┐
                     │  control plane  │  ◄── parent app, or examples/ui (service token)
                     │  Go + SQLite    │  ◄── browser  (attach token → wss)
                     └─────────────────┘  ◄── browser  (preview cookie → https)
```

## Sandbox lifecycle

```
POST /sandboxes ──► queued ──► creating ──► running ──► ended
                      ▲                        │          reason ∈
               no free slot        host_offline flag     closed | idle | failed | lost
```

- **Placement:** among online + approved hosts whose tag set contains every tag the
  sandbox requires, the one with most free slots (`max - running` from heartbeat).
  None → `queued`, FIFO, persisted. Drains on every heartbeat and on every sandbox end.
  A requirement no approved host in the fleet carries at all is refused at create (422)
  rather than queued forever; being merely full or offline still queues.
- **Creating:** CP sends `sandbox.create` with image, cmd, env, secret_env and
  idle timeout. Daemon: create the container → exec cmd (or the image's default entry)
  in a PTY with that env → `sandbox.started`. Any step failing removes the
  container and ends the sandbox `failed`.
- **Running:** daemon owns the PTY regardless of viewers. Viewers attach via CP;
  attach = ring-buffer replay, then live. N viewers fan out; last resize wins.
- **Idle:** daemon tracks `last_activity` = last byte in _or_ out of the PTY.
  Viewer attach/detach and preview traffic do **not** count. After
  `idle_timeout` (default 30 m) the daemon ends the sandbox.
- **Ended:** `DELETE /sandboxes/:id` (`closed`), idle (`idle`), container or
  clone failure (`failed`), host never came back (`lost`). The container is removed.
- **Host offline:** the container keeps running; CP flags the sandbox; the daemon's
  idle timer keeps running. On reconnect the daemon's `hello.running` list
  reconciles: present → running, absent → `lost`/`idle` as reported.
- **CP restart:** open db, mark `creating|running` as unknown, wait for
  `hello`s to reconcile, then drain `queued`.

## Trust

- **Parent → CP:** `Authorization: Bearer <SANDBOXD_SERVICE_TOKEN>`. Every request
  carries `owner_id`; CP enforces `sandbox.owner_id == owner_id`. CP has no
  user table.
- **Browser → terminal:** parent calls `POST /sandboxes/:id/attach-token`
  (60 s, single sandbox). Browser opens `wss://cp/attach?token=…`.
- **Browser → preview:** parent calls `POST /sandboxes/:id/preview-token`
  (10 m). Browser hits `https://3000-s_x.preview.<domain>/?t=…`; CP verifies,
  sets a signed, subdomain-scoped cookie, redirects to `/`. The cookie also
  gates WebSocket upgrades; the upstream never sees it. The proxy forwards
  `x-forwarded-host` / `x-forwarded-proto` so apps can reconstruct the URL.
- **Secrets:** anything in `secret_env` (the coding-agent preset puts
  `secrets.git_token`, `secrets.anthropic_api_key`, `llm.api_key` there).
  Forwarded to the daemon, injected as env into the PTY process. Not written to
  SQLite, not logged, dropped from memory after `docker exec`. Operator-level
  `SANDBOXD_SANDBOX_ENV_*` (and the `LLM_BASE_URL` / `LLM_API_KEY` shorthands)
  travel the same path and are never persisted either.
- **Enrollment:** daemon generates `host_secret` on first run
  (`~/.config/sandboxd/host.json`, 0600). `hello{name, fingerprint}` where
  `fingerprint = sha256(secret)`. Unknown fingerprint → CP inserts
  `status:pending`, mints code `XXXX-XX`, replies `pending{code}`; daemon
  prints it and waits. Admin: `POST /hosts/:id/approve {code}`. Next hello is
  accepted. Revoke = `status:revoked`. `SANDBOXD_AUTO_APPROVE` does not exist in v1.
- **Join token (added 2026-09-03):** when the CP has `SANDBOXD_JOIN_TOKEN` and the
  `hello` carries a matching `join_token`, an unknown fingerprint is inserted
  `status:approved` and a pending one is approved, with no code. A wrong token is
  ignored (the hello takes the pending path, the CP logs it); a revoked host stays
  revoked whatever it presents. The token travels only in the hello, is compared in
  constant time, and is never logged or stored. Without the variable on the CP the
  field is inert.
- **Git:** HTTPS clone; token supplied via `GIT_ASKPASS` helper. No SSH.

## Wire protocol (host ↔ CP)

One WebSocket. **Text frames** are JSON control messages. **Binary frames**
are `[u32 BE streamId][payload]`; a stream carries either PTY bytes for one
sandbox or one proxied TCP connection for a preview port.

| direction | message                                                                                   |
| --------- | ----------------------------------------------------------------------------------------- |
| host→cp   | `hello{name, fingerprint, running: sid[], max_sandboxes, tags: string[], join_token?}`   |
| cp→host   | `hello.ok{host_id}` · `hello.pending{code}` · `hello.rejected`                            |
| host→cp   | `heartbeat{running, max}` every 10 s                                                      |
| cp→host   | `sandbox.create{sid, image, cmd \| null, idle_timeout_s, env, secret_env}`                |
| host→cp   | `sandbox.started{sid}` · `sandbox.ended{sid, reason}`                                     |
| cp→host   | `sandbox.destroy{sid}`                                                                    |
| cp→host   | `pty.open{sid, stream, cols, rows}` · `pty.resize{sid, cols, rows}` · `pty.close{stream}` |
| host→cp   | `pty.replay{stream}` (binary follows)                                                     |
| cp→host   | `port.dial{sid, port, stream}` · `port.close{stream}`                                     |
| host→cp   | `port.open{stream}` · `port.error{stream, msg}` · `port.close{stream}`                    |

Stream ids are allocated by the CP (odd) and never reused within a connection. `sid` stays
the field name for a sandbox id (decision 5): the resource was renamed, the field was not.

## HTTP API (parent → CP)

The API is generic: a body names an image, a command and env. Presets and the browser
page live in `examples/ui`, the reference client, which turns a friendly request into this. Every request and response shape is declared once in
`internal/cp/api.go` (Go structs with tags) and read by huma for validation, for `GET /openapi.json`
(rendered at `GET /doc`) and by the example client. Errors are `{error, issues?}`; a
validation failure lists every failing field. Unknown body keys are a 400, so a `repo`
or `preset` sent here says the caller meant the UI.

```
GET    /healthz  /openapi.json  /doc    public
GET    /hosts                           list (status, online, running/max, tags)
POST   /hosts/:id/approve   {code}
POST   /hosts/:id/revoke
POST   /sandboxes           {owner_id, image, cmd?: string[], env?, secret_env?, idle_timeout_s?, tags?: string[]}
                            tags: every one must be present on a host for it to be a candidate.
                            422 when no approved host carries them all; queues when they are just full.
                            TERM and SANDBOXD_SESSION_ID are reserved env names.
                            Operator defaults for every sandbox: SANDBOXD_SANDBOX_ENV_<NAME>=value,
                            shorthands SANDBOXD_LLM_BASE_URL / SANDBOXD_LLM_API_KEY (or LLM_BASE_URL / LLM_API_KEY).
                            Precedence: secret_env > env > operator > entry.sh default.
GET    /sandboxes?owner_id=
GET    /sandboxes/:id?owner_id=
DELETE /sandboxes/:id?owner_id=
POST   /sandboxes/:id/attach-token  {owner_id}       → {token, wss_url, expires_in_s}
POST   /sandboxes/:id/preview-token {owner_id, port} → {token, url, expires_in_s}
GET    /attach?token=                   WebSocket (xterm ↔ pty); public, the token is the gate
*      <port>-<sid>.preview.<domain>/*  preview proxy (HTTP + WebSocket)
```

## UI (parent-app stand-in, `examples/ui`)

Holds the service token and the presets (`examples/ui/presets/<name>/preset.yaml`). Serves
the xterm.js page at `/`. Talks to the API with `fetch`; the browser never
holds the token.

```
GET    /presets                         [{name, description, image, cmd, idle_timeout_s, preview: {port, port_field},
                                          fields: [{name, type, env, required, secret, default?, values?, min?, max?, multiline?, description?}]}]
POST   /sandboxes           {owner_id, preset?, image?, cmd?, env?, secret_env?, idle_timeout_s?, …preset fields}
                            presets: `preset` names a folder; without it, the first preset whose `claims_when` fields are
                                    present wins (coding-agent claims `repo`), else `custom`. Each preset's fields map onto
                                    env for its image; absent fields emit nothing, so operator defaults survive. Shipped:
                                    coding-agent  {repo, prompt?, agent?, model?, setup?, branch?, base_branch?, llm?: {base_url?, api_key?},
                                                   secrets?: {git_token?, anthropic_api_key?}}
                                                  → env REPO PROMPT AGENT MODEL SETUP BRANCH BASE_BRANCH LLM_BASE_URL,
                                                    secret_env GIT_TOKEN ANTHROPIC_API_KEY LLM_API_KEY; image sandboxd-coding-agent
                                                  entry.sh defaults: agent = claude (shell if prompt empty), model = claude-sonnet-4-6
                                                  (CODEX_MODEL for codex), branch = sandboxd/<sid>
                                    vscode        {repo?, port? (8080), secrets?: {git_token?}} → env REPO VSCODE_PORT; image sandboxd-vscode
                                                  (code-server, auth off: the preview cookie is the gate); idle 4 h; preview = port
                                    jupyter       {repo?, ui?: lab|notebook, port? (8888), secrets?: {git_token?}} → env REPO JUPYTER_UI
                                                  JUPYTER_PORT; image sandboxd-jupyter (docker-stacks + entry); idle 4 h; preview = port
                                    custom        nothing implied; image from the caller, else SANDBOXD_DEFAULT_IMAGE (of the UI)
                            a preset may default image / cmd / idle_timeout_s; caller-supplied values win;
                            caller env/secret_env are merged over the preset's. The result is POST /sandboxes on the API.
*                                       everything else is forwarded to the API with the service token added
```

Browser attach WS: binary frames = PTY bytes both ways; text frame
`{"type":"resize","cols","rows"}` from browser.

## Sandbox

**Contract with any image:** the daemon starts the container with the image's
own entrypoint kept idle (`sleep infinity`), then execs `cmd` (default:
`/usr/local/bin/sandboxd-entry`, overridable per host with
`SANDBOXD_WORKER_ENTRY`) in a PTY with the sandbox's env plus `TERM` and
`SANDBOXD_SESSION_ID` (that name is kept deliberately — decision 5). When that process
exits the sandbox ends (`exited`).
Any TCP port it listens on can be previewed. That is all the daemon assumes;
what the env means is between the caller and the image.

The sandbox sits on Docker's default bridge; `host.docker.internal` resolves to the
host. Orphans from a previous daemon run are found by label and removed on start.

**Presets** are folders: `presets/<name>/preset.yaml` (description, image, idle,
preview port, fields → env; validated at boot, see
`examples/ui/src/presets/schema.ts`), a `Dockerfile` (built by `bun run image` as the image
the preset declares) and the scripts it copies in. Every image installs its entry at
`/usr/local/bin/sandboxd-entry`, so `cmd` stays null. Field defaults that an
operator should be able to override are _not_ in the yaml: the entry script applies
them when the env is unset, and `SANDBOXD_SANDBOX_ENV_*` reaches the entry untouched
because absent fields emit no env.

`presets/coding-agent/Dockerfile`: `debian:bookworm-slim` + git, curl, node, bun,
`@anthropic-ai/claude-code`, `@openai/codex`, `opencode-ai`; user `dev`; `WORKDIR /workspace`.
Entry (`entry.sh`, executed in the PTY): default AGENT/MODEL → clone → `checkout -b $BRANCH` →
`SETUP` → launch the chosen agent → always fall through to `bash` so the user can inspect
or `git push`. Empty prompt = `shell`. `presets/vscode/Dockerfile`: `debian:bookworm-slim` +
git + code-server; entry clones and execs `code-server --auth none` on `VSCODE_PORT`
(the preview proxy's `x-forwarded-host` satisfies its origin check; `--trusted-origins '*'`
as a fallback). `presets/jupyter/Dockerfile`: `quay.io/jupyter/minimal-notebook` + the entry,
run as jovyan. Gateway mapping (`LLM_BASE_URL` root, no `/v1`):

| agent                        | how the gateway is wired                                                                                                |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| claude                       | `ANTHROPIC_BASE_URL=$BASE`, `ANTHROPIC_AUTH_TOKEN=$LLM_API_KEY`, `ANTHROPIC_MODEL=$MODEL`                               |
| codex                        | `~/.codex/config.toml` with `model_providers.gateway.base_url=$BASE/v1`, `env_key=OPENAI_API_KEY`, approvals off        |
| opencode                     | `~/.config/opencode/opencode.json` provider `gateway` (`@ai-sdk/openai-compatible`, `$BASE/v1`), `model=gateway/$MODEL` | Container: no privileged, default bridge network, container |
| IP used for preview dialing. |

`SandboxDriver` interface (only `docker` implemented):

```ts
create({sid, image}): Promise<id>                // the image's entrypoint kept idle (sleep infinity)
attach(id, cmd, env, size): Promise<PtyStream>   // docker exec Tty:true, hijacked; resize lives on the stream
dial(id, port): Promise<Duplex>                  // TCP to container ip:port, for the preview proxy
destroy(id): Promise<void>
listManaged(): Managed[]                         // by label, for orphan cleanup
```

## Storage (SQLite)

```
hosts     (id, name, fingerprint UNIQUE, status pending|approved|revoked,
           approve_code, max_sandboxes, tags (JSON string[]), last_seen_at, created_at)
sandboxes (id, owner_id, host_id NULL, status, ended_reason NULL, ended_detail NULL,
           image, cmd NULL (JSON string[]), env (JSON object), tags (JSON string[]),
           idle_timeout_s, created_at, started_at, ended_at, unknown_since NULL)
PRAGMA user_version = 6   -- no migrations in v1; another version is wiped at boot (revised 2026-09-04)
```

`hosts.tags` is what the host last reported in its `hello`, cached so the CP can answer
`GET /hosts` and reject unsatisfiable placements while a host is offline. `sandboxes.tags`
is the requirement the caller asked for, kept because placement can happen long after
create.

No terminal bytes. No secrets. Queue position is derived from `created_at`
among `queued` rows. Because secrets are never persisted, `queued` sandboxes cannot
survive a CP restart: on boot they are marked `ended/failed` and must be recreated.
A db from another schema version is dropped and recreated with a warning, not refused:
every row is disposable (sandboxes are ephemeral, hosts re-enroll on their next hello),
and a deployed unit under `Restart=always` must never crash-loop on a version bump.

## Repo layout

One Go module, and one example client that is not part of it.

```
internal/wire/          messages.go  framing.go  ids.go  + testdata/    the wire contract; a leaf
internal/cp/            errors.go  api.go  config.go  env.go  tokens.go  scheduler.go  sandboxes.go
internal/cp/store/      model.go  sqlite.go                     the only package that speaks SQL
internal/cp/hosts/      conn.go  stream.go  hub.go  enrollment.go  service.go   one Conn per worker; Stream is a net.Conn
internal/cp/attach/     bridge.go                                browser terminal <-> PTY
internal/cp/preview/    proxy.go                                 ReverseProxy over a tunnel stream
internal/cp/http/       routes.go  sandbox.go  host.go  service.go  errors.go   huma; the only place a status code lives
internal/worker/        config.go  ring.go  sandboxes.go  tunnel.go
internal/worker/driver/ driver.go  docker.go                    (kubernetes.go next)
cmd/sandboxd-api/      main.go                                  wiring only
cmd/sandboxd-worker/   main.go                                  wiring only
scripts/dev/           main.go                                  the whole stack in one terminal; not shipped
examples/ui/src/       main.ts  config.ts  app.ts  sandboxes.ts  index.html  errors.ts  json.ts  log.ts
examples/ui/src/presets/ index.ts  types.ts  schema.ts  preset.ts  loader.ts   yaml -> Preset; registry
examples/ui/presets/   build.ts                                 one image per preset
examples/ui/presets/<name>/ preset.yaml  Dockerfile  entry.sh  …   coding-agent  vscode  jupyter  custom (no image)
<pkg>_test.go          beside the source it tests; examples/ui/test/ for the client
```

Commands: `go build ./...`, `go test ./...`, `golangci-lint run`, `go run ./scripts/dev`,
`nix build .#api`. In `examples/ui`: `bun run dev`, `bun run image [preset…]`, `bun test`.

## Explicitly out of scope for v1

1. Structured agent events, chat UI, push notifications
2. Multiple CP instances (socket-affinity problem) — no Redis
3. Resource-based scheduling (CPU/memory/GPU requests). Tags are in as of 2026-09-04
   (decision 8) but they are set membership, not quantities
4. Central secret store; host-level secrets
5. Workspaces / shared filesystem across sandboxes
6. Patch export / read-only-clone mode
7. Persistent scrollback; audit log
8. SSH-key git auth; SSH fallback transport
9. Clone/dependency caching; snapshots
10. Podman/Firecracker drivers (interface only)
11. mTLS; unconditional auto-approve (the join token landed 2026-09-03, see decision 12)
12. Any real frontend
