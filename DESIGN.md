# sandboxd — prototype design

Status: **signed 2026-09-01**. This is the v1 contract. Change it here first.

## What it is

A microservice that lets a parent application run **agent tasks** on a fleet of
self-hosted machines and supervise them through a browser terminal.

- A **host** is a real machine you own, running the `sandboxd` worker. It dials **out** to
  the control plane over one persistent WebSocket; nothing inbound is required.
- A **session** is one sandbox: a fresh Docker container from an image, one
  command started in a PTY with some env, gone when it ends. Disposable.
  It may bring **services**: sidecar containers (Postgres, Redis, …) on a
  private per-session network, reachable from the sandbox by name, started
  before it and removed with it. The core does not know what runs inside. **Presets** turn a
  friendly request (repo + prompt + agent) into image/cmd/env. A preset is a folder
  of data, `presets/<name>/` (`preset.yaml` + `Dockerfile` + `entry.sh`), with its own
  image: `coding-agent` is the reason this service exists, `vscode` opens a repo in
  VS Code behind the preview proxy, `jupyter` does the same for a notebook server,
  and any image that honours the sandbox contract below is a valid use case.
  Services come from a **catalog** (`presets/services.yaml`, a compose file) or from
  a compose document the caller sends as-is.
- The control plane (**CP**) is the only API. It schedules sessions onto hosts,
  relays terminal bytes, proxies preview ports, and stores metadata. It is a
  single instance backed by SQLite and has no users of its own.

## Decisions (with the alternative that was rejected)

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | Rejected                                                                                                                                                                                                                          |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Worker dials out; one wss per host, multiplexed                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | CP dials in via SSH (dead behind NAT, no free preview URLs)                                                                                                                                                                       |
| 2   | Hosts are real machines; sandbox lib is a host-local driver                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | "Hosts" as sandbox-provider pointers (no PTY, vendor cost)                                                                                                                                                                        |
| 3   | One channel: raw PTY bytes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Second structured agent-event channel (2× protocol, harness coupling)                                                                                                                                                             |
| 4   | Daemon owns PTY + 256 KB ring buffer; CP stores no bytes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | tmux (TUI-in-TUI); CP-owned buffer (secrets at rest)                                                                                                                                                                              |
| 5   | Session = one task = one sandbox                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Workspace with N terminals (not the product being built)                                                                                                                                                                          |
| 6   | CP trusts parent app: service token + `owner_id`; short-lived attach tokens for browsers                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | CP validates parent JWT; CP owns users                                                                                                                                                                                            |
| 7   | Secrets passed per-session, forwarded, never persisted                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Central secret store; host-level secrets                                                                                                                                                                                          |
| 8   | CP auto-picks host by free slots; FIFO queue when full                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Caller picks host; resource-based; labels                                                                                                                                                                                         |
| 9   | Output = `git push` from inside the sandbox                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Patch export with read-only clone                                                                                                                                                                                                 |
| 10  | Idle timeout: any PTY byte either direction resets; 30 m default                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Client-input-only; count preview traffic                                                                                                                                                                                          |
| 11  | Preview: `<port>-<sid>.preview.<domain>` + signed cookie; WebSocket upgrades bridged (frames re-encoded at the CP)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Path-based (breaks absolute paths); unauthenticated; HTTP-only preview (added 2026-09-02: Jupyter kernels need it)                                                                                                                |
| 12  | Enrollment: daemon self-generates secret, CP sees fingerprint, pending + short code, admin approves. Revised 2026-09-03: an optional join token (`SANDBOXD_JOIN_TOKEN` on both sides) skips the code — the operator who provisions the worker already holds it. The fingerprint stays the host's identity; the token only decides who approves.                                                                                                                                                                                                                                                                 | Static shared secret as identity; mTLS; unconditional auto-approve                                                                                                                                                                |
| 13  | SQLite only; daemon is source of truth for running state                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | Redis (solves nothing without multi-instance)                                                                                                                                                                                     |
| 14  | Raw Docker Engine API behind `SandboxDriver`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Sandcastle / TanStack sandbox (they want to own the agent run)                                                                                                                                                                    |
| 15  | Presets are data: `presets/<name>/preset.yaml` + `Dockerfile` + `entry.sh`, one image per preset (`sandboxd-<name>:latest`), `image:` override per session (revised 2026-09-03); presets live in `apps/ui` and the API never sees one — it takes image, cmd, env, sidecars (revised again 2026-09-03)                                                                                                                                                                                                                                                                                                           | One image for every preset; presets as TypeScript modules; caller-supplied image always; per-host image                                                                                                                           |
| 19  | Agent is a per-session choice (`claude` \| `codex` \| `opencode` \| `shell`); one LLM gateway tuple (`base_url`, `api_key`, `model`) is mapped by the entry script onto each harness's own config; defaults (agent, model) live in the image's entry and operators override them with `SANDBOXD_SANDBOX_ENV_*`                                                                                                                                                                                                                                                                                                  | Claude-only; per-agent config schemas in the API; per-preset env vars on the CP                                                                                                                                                   |
| 16  | Bun only, one runtime (revised 2026-09-03): `Bun.serve` owns the sockets and the preview proxy, `bun:sqlite` the state; Hono is adopted for the parent-facing JSON API (validation, typed routes, an OpenAPI document) — pending until the API rewrite lands                                                                                                                                                                                                                                                                                                                                                    | Elysia; zero runtime deps; multi-runtime targets (Workers, Lambda): the CP is a stateful single instance                                                                                                                          |
| 17  | `apps/ui` (revised 2026-09-03): a Bun + Hono app that serves the xterm.js page at `/`, holds the service token, resolves presets, and forwards everything else to the API. The parent-app stand-in.                                                                                                                                                                                                                                                                                                                                                                                                             | No UI; real Svelte frontend; a `/dev` page inside the control plane                                                                                                                                                               |
| 18  | Bun workspaces (revised 2026-09-03): `apps/api`, `apps/ui`, `apps/worker`, `packages/core`; the worker keeps an empty dependency set; fallow enforces api → core, worker → core, ui → core + api (types and the typed client), never the reverse                                                                                                                                                                                                                                                                                                                                                                | One package, `src/{protocol,shared,cp,agent,dev}`                                                                                                                                                                                 |
| 20  | Generic core (`image, cmd, env, secret_env, services`) + presets as a declarative field→env schema; the image is the plugin, its entry script owns the semantics. The generic half is the API, the preset half is the UI (revised 2026-09-03)                                                                                                                                                                                                                                                                                                                                                                   | Agent kinds baked into the protocol and daemon; driver plugins in the daemon; per-preset code on the CP                                                                                                                           |
| 21  | Session may declare `services` (added 2026-09-03): sidecar containers on a per-session bridge network. Given to the API as a **compose document** it translates itself (validated subset: image, environment, command, expose/ports → readiness port, depends_on → order; laptop keys ignored, host-affecting keys refused), or fully formed. Catalog names (`apps/ui/presets/services.yaml`) are the UI's: it copies the picked entries into that compose document (revised 2026-09-03). The CP fetches nothing from repos; the parent app forwards the repo's compose file if it wants repo-authored services | CP reads `.sandboxd.yaml` from the repo (provider APIs + a trust tier for repo-authored config); compose as the runtime (`docker compose up` per session: plugin on every host, still needs filtering); docker inside the sandbox |

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
                     │  control plane  │  ◄── parent app, or apps/ui (service token)
                     │  Bun + SQLite   │  ◄── browser  (attach token → wss)
                     └─────────────────┘  ◄── browser  (preview cookie → https)
```

## Session lifecycle

```
POST /sessions ──► queued ──► creating ──► running ──► ended
                     ▲                        │          reason ∈
              no free slot         host_offline flag     closed | idle | failed | lost
```

- **Placement:** online + approved host with most free slots (`max - running`
  from heartbeat). None → `queued`, FIFO, persisted. Drains on every heartbeat
  and on every session end.
- **Creating:** CP sends `session.create` with image, cmd, env, secret_env,
  services and idle timeout. Daemon: (if services) create network → start each
  service in order, waiting on its `ready` TCP port → create sandbox → exec cmd
  (or the image's default entry) in a PTY with that env → `session.started`.
  Any step failing removes what was started and ends the session `failed`.
- **Running:** daemon owns the PTY regardless of viewers. Viewers attach via CP;
  attach = ring-buffer replay, then live. N viewers fan out; last resize wins.
- **Idle:** daemon tracks `last_activity` = last byte in _or_ out of the PTY.
  Viewer attach/detach and preview traffic do **not** count. After
  `idle_timeout` (default 30 m) the daemon ends the session.
- **Ended:** `DELETE /sessions/:id` (`closed`), idle (`idle`), container or
  clone failure (`failed`), host never came back (`lost`). Sandbox is removed.
- **Host offline:** sandbox keeps running; CP flags the session; the daemon's
  idle timer keeps running. On reconnect the daemon's `hello.running` list
  reconciles: present → running, absent → `lost`/`idle` as reported.
- **CP restart:** open db, mark `creating|running` as unknown, wait for
  `hello`s to reconcile, then drain `queued`.

## Trust

- **Parent → CP:** `Authorization: Bearer <SANDBOXD_SERVICE_TOKEN>`. Every request
  carries `owner_id`; CP enforces `session.owner_id == owner_id`. CP has no
  user table.
- **Browser → terminal:** parent calls `POST /sessions/:id/attach-token`
  (60 s, single session). Browser opens `wss://cp/attach?token=…`.
- **Browser → preview:** parent calls `POST /sessions/:id/preview-token`
  (10 m). Browser hits `https://3000-s_x.preview.<domain>/?t=…`; CP verifies,
  sets a signed, subdomain-scoped cookie, redirects to `/`. The cookie also
  gates WebSocket upgrades; the upstream never sees it. The proxy forwards
  `x-forwarded-host` / `x-forwarded-proto` so apps can reconstruct the URL.
- **Secrets:** anything in `secret_env` (the coding-agent preset puts
  `secrets.git_token`, `secrets.anthropic_api_key`, `llm.api_key` there).
  Forwarded to the daemon, injected as env into the PTY process. Not written to
  SQLite, not logged, dropped from memory after `docker exec`. Operator-level
  `SANDBOXD_SANDBOX_ENV_*` (and the `LLM_BASE_URL` / `LLM_API_KEY` shorthands)
  travel the same path and are never persisted either. A service's `secret_env`
  is weaker by necessity: it is set at container create (there is no exec step),
  so it is visible in `docker inspect` on the host. Still never stored on the CP.
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
session or one proxied TCP connection for a preview port.

| direction | message                                                                                   |
| --------- | ----------------------------------------------------------------------------------------- |
| host→cp   | `hello{name, fingerprint, running: sid[], max_sessions, join_token?}`                     |
| cp→host   | `hello.ok{host_id}` · `hello.pending{code}` · `hello.rejected`                            |
| host→cp   | `heartbeat{running, max}` every 10 s                                                      |
| cp→host   | `session.create{sid, image, cmd                                                           | null, idle_timeout_s, env, secret_env, services: [{name, image, env, secret_env, cmd | null, ready: {port, timeout_s} | null}]}` |
| host→cp   | `session.started{sid}` · `session.ended{sid, reason}`                                     |
| cp→host   | `session.destroy{sid}`                                                                    |
| cp→host   | `pty.open{sid, stream, cols, rows}` · `pty.resize{sid, cols, rows}` · `pty.close{stream}` |
| host→cp   | `pty.replay{stream}` (binary follows)                                                     |
| cp→host   | `port.dial{sid, port, stream}` · `port.close{stream}`                                     |
| host→cp   | `port.open{stream}` · `port.error{stream, msg}` · `port.close{stream}`                    |

Stream ids are allocated by the CP (odd) and never reused within a connection.

## HTTP API (parent → CP)

The API is generic: a body names an image, a command, env and sidecars. Presets, the
service catalog and the browser page live in `apps/ui`, the parent-app stand-in, which
turns a friendly request into this. Every request and response shape is declared once in
`apps/api/src/schema.ts` (Zod) and read by the validator, by `GET /openapi.json`
(rendered at `GET /doc`) and by the UI's typed client. Errors are `{error, issues?}`; a
validation failure lists every failing field. Unknown body keys are a 400, so a `repo`
or `preset` sent here says the caller meant the UI.

```
GET    /healthz  /openapi.json  /doc    public
GET    /hosts                           list (status, online, running/max)
POST   /hosts/:id/approve   {code}
POST   /hosts/:id/revoke
POST   /sessions            {owner_id, image, cmd?: string[], env?, secret_env?, idle_timeout_s?,
                             services?: [{name, image, env?, secret_env?, cmd?, ready?: {port, timeout_s?}}],
                             compose?: string | object}
                            services: sidecars on a private per-session network, reachable from the sandbox as `name`
                                    (DNS label, `sandbox` reserved). Started in order before the sandbox; `ready` blocks
                                    until TCP `port` accepts (timeout default 60 s, max 600) or fails the session.
                                    Sources, in start order: the `services` list, then the `compose` document. Names must
                                    be unique across both; at most SANDBOXD_MAX_SERVICES (8). Non-secret halves are
                                    persisted and echoed in the session view.
                            compose: a docker compose file, verbatim (YAML text or object). Per service: `build` without `image`
                                    = the repo's own app, skipped; `image`, `environment` (map or list, `${X:-d}` interpolated
                                    against an empty env), `command`, `depends_on` (order), readiness = `x-sandboxd.ready`
                                    else `expose[0]` else the container port of `ports[0]`; `x-sandboxd.sandbox_env` is
                                    merged under the session env, `x-sandboxd.secret_env` is never persisted. Ignored: ports,
                                    volumes, restart, healthcheck, networks, labels, deploy, …. Refused (400 naming the key):
                                    entrypoint, privileged, cap_add, devices, network_mode, pid, user, extra_hosts, sysctls,
                                    security_opt, volumes_from, extends, … (compose.ts).
                            TERM and SANDBOXD_SESSION_ID are reserved env names.
                            Operator defaults for every sandbox: SANDBOXD_SANDBOX_ENV_<NAME>=value,
                            shorthands SANDBOXD_LLM_BASE_URL / SANDBOXD_LLM_API_KEY (or LLM_BASE_URL / LLM_API_KEY).
                            Precedence: secret_env > env > services' sandbox_env > operator > entry.sh default.
GET    /sessions?owner_id=
GET    /sessions/:id?owner_id=
DELETE /sessions/:id?owner_id=
POST   /sessions/:id/attach-token  {owner_id}       → {token, wss_url, expires_in_s}
POST   /sessions/:id/preview-token {owner_id, port} → {token, url, expires_in_s}
GET    /attach?token=                   WebSocket (xterm ↔ pty); public, the token is the gate
*      <port>-<sid>.preview.<domain>/*  preview proxy (HTTP + WebSocket)
```

## UI (parent-app stand-in, `apps/ui`)

Holds the service token, the presets (`apps/ui/presets/<name>/preset.yaml`) and the
catalog (`apps/ui/presets/services.yaml`). Serves the xterm.js page at `/`. Talks to the
API through `hono/client`, typed off the API's routes; the browser never holds the token.

```
GET    /presets                         [{name, description, image, cmd, idle_timeout_s, preview: {port, port_field}, services,
                                          fields: [{name, type, env, required, secret, default?, values?, min?, max?, multiline?, description?}]}]
GET    /services                        [{name, image, ready, sandbox_env}]   the catalog
POST   /sessions            {owner_id, preset?, image?, cmd?, env?, secret_env?, idle_timeout_s?,
                             services?: [name | {use, name?, env?, secret_env?} | full declaration], compose?, …preset fields}
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
                            a preset may default image / cmd / idle_timeout_s / services; caller-supplied values win;
                            caller env/secret_env are merged over the preset's.
                            services: catalog names and references are copied into one compose document together with the
                                    caller's own file, in start order: the preset's defaults, the `services` picks, the file.
                                    Full declarations pass through as `services`. The result is POST /sessions on the API.
*                                       everything else is forwarded to the API with the service token added
```

Browser attach WS: binary frames = PTY bytes both ways; text frame
`{"type":"resize","cols","rows"}` from browser.

## Sandbox

**Contract with any image:** the daemon starts the container with the image's
own entrypoint kept idle (`sleep infinity`), then execs `cmd` (default:
`/usr/local/bin/sandboxd-entry`, overridable per host with
`SANDBOXD_WORKER_ENTRY`) in a PTY with the session's env plus `TERM` and
`SANDBOXD_SESSION_ID`. When that process exits the session ends (`exited`).
Any TCP port it listens on can be previewed. That is all the daemon assumes;
what the env means is between the caller and the image.

**Services** are ordinary containers: the image's own command (or `cmd`), `env` set at
create, joined to the session's bridge network `sandboxd-<sid>` under their
`name` as DNS alias (the sandbox is `sandbox`). A session without services stays
on Docker's default bridge. Readiness = the daemon dialling `ready.port` every
500 ms until it accepts. Teardown: sandbox, services in reverse order, network.
Orphans from a previous daemon run are found by label and removed on start.

**Presets** are folders: `presets/<name>/preset.yaml` (description, image, idle,
preview port, default services, fields → env; validated at boot, see
`apps/ui/src/presets/schema.ts`), a `Dockerfile` (built by `bun run image` as the image
the preset declares) and the scripts it copies in. Every image installs its entry at
`/usr/local/bin/sandboxd-entry`, so `cmd` stays null. Field defaults that an
operator should be able to override are _not_ in the yaml: the entry script applies
them when the env is unset, and `SANDBOXD_SANDBOX_ENV_*` reaches the entry untouched
because absent fields emit no env. `presets/services.yaml` is the catalog, an
ordinary compose file read through `apps/api/src/compose.ts`.

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
create({sid, image, role: sandbox|service, network?, alias?, env?, cmd?}): Promise<id>
attach(id, cmd, env, size): Promise<PtyStream>   // docker exec Tty:true, hijacked; resize lives on the stream
dial(id, port): Promise<Duplex>                  // TCP to container ip:port; also the readiness probe
destroy(id): Promise<void>
createNetwork(sid): Promise<name> · removeNetwork(sid)
listManaged(): {containers, networks}            // by label, for orphan cleanup
```

## Storage (SQLite)

```
hosts    (id, name, fingerprint UNIQUE, status pending|approved|revoked,
          approve_code, max_sessions, last_seen_at, created_at)
sessions (id, owner_id, host_id NULL, status, ended_reason NULL, ended_detail NULL,
          preset, image, cmd NULL (JSON string[]), env (JSON object),
          services (JSON [{name, image, env, cmd, ready}], secrets stripped), idle_timeout_s,
          created_at, started_at, ended_at, unknown_since NULL)
PRAGMA user_version = 3   -- no migrations in v1; another version is refused at boot
```

No terminal bytes. No secrets. Queue position is derived from `created_at`
among `queued` rows. Because secrets are never persisted, `queued` sessions cannot
survive a CP restart: on boot they are marked `ended/failed` and must be recreated.

## Repo layout

```
packages/core/src/     messages.ts  framing.ts  ids.ts  log.ts  bytes.ts  errors.ts   the wire contract; imported as @sandboxd/core/<file>
apps/api/src/          main.ts  config.ts  schema.ts  store.ts  tokens.ts  scheduler.ts  sessions.ts  services.ts  compose.ts  env.ts  attach.ts
apps/api/src/http/     index.ts  routes.ts  common.ts  doc.ts  handler/{session,host,service}.ts    Hono; one handler per resource
apps/api/src/hosts/    conn.ts  enrollment.ts  hub.ts  service.ts    one HostConn per worker; hub = registry + narrow interfaces
apps/api/src/preview/  proxy.ts  http.ts  ws-bridge.ts  response-parser.ts  wsframe.ts
apps/ui/src/           main.ts  config.ts  app.ts  sessions.ts  services.ts  index.html    presets → API body; the page; the forwarder
apps/ui/src/presets/   index.ts  types.ts  schema.ts  preset.ts  loader.ts  catalog.ts    yaml → Preset; registry; service catalog
apps/ui/presets/       build.ts  services.yaml                       one image per preset; the catalog is a compose file
apps/ui/presets/<name>/ preset.yaml  Dockerfile  entry.sh  …         coding-agent  vscode  jupyter  custom (no image)
apps/worker/src/       main.ts  config.ts  tunnel.ts  driver.ts  docker.ts  pty.ts  sessions.ts
<package>/test/        bun:test, next to the package it covers
```

Scripts: `bun run api`, `bun run ui`, `bun run worker`, `bun run image [preset…]`, `bun run test`,
`bun run typecheck`, `bun run fmt`, `bun run lint`, `bun run fallow`.

## Explicitly out of scope for v1

1. Structured agent events, chat UI, push notifications
2. Multiple CP instances (socket-affinity problem) — no Redis
3. Resource-based scheduling; labels/selectors
4. Central secret store; host-level secrets
5. Workspaces / shared filesystem across sessions
6. Patch export / read-only-clone mode
7. Persistent scrollback; audit log
8. SSH-key git auth; SSH fallback transport
9. Clone/dependency caching; snapshots
10. Podman/Firecracker drivers (interface only)
11. mTLS; unconditional auto-approve (the join token landed 2026-09-03, see decision 12)
12. Any real frontend
