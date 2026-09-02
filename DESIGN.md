# devagents — prototype design

Status: **signed 2026-09-01**. This is the v1 contract. Change it here first.

## What it is

A microservice that lets a parent application run **agent tasks** on a fleet of
self-hosted machines and supervise them through a browser terminal.

- A **host** is a real machine you own, running `devagents`. It dials **out** to
  the control plane over one persistent WebSocket; nothing inbound is required.
- A **session** is one agent task: a fresh Docker sandbox, a fresh clone of a
  repo on its own branch, `claude "<prompt>"` started in a PTY. Disposable.
- The control plane (**CP**) is the only API. It schedules sessions onto hosts,
  relays terminal bytes, proxies preview ports, and stores metadata. It is a
  single instance backed by SQLite and has no users of its own.

## Decisions (with the alternative that was rejected)

| # | Decision | Rejected |
|---|----------|----------|
| 1 | Worker dials out; one wss per host, multiplexed | CP dials in via SSH (dead behind NAT, no free preview URLs) |
| 2 | Hosts are real machines; sandbox lib is a host-local driver | "Hosts" as sandbox-provider pointers (no PTY, vendor cost) |
| 3 | One channel: raw PTY bytes | Second structured agent-event channel (2× protocol, harness coupling) |
| 4 | Daemon owns PTY + 256 KB ring buffer; CP stores no bytes | tmux (TUI-in-TUI); CP-owned buffer (secrets at rest) |
| 5 | Session = one task = one sandbox | Workspace with N terminals (not the product being built) |
| 6 | CP trusts parent app: service token + `owner_id`; short-lived attach tokens for browsers | CP validates parent JWT; CP owns users |
| 7 | Secrets passed per-session, forwarded, never persisted | Central secret store; host-level secrets |
| 8 | CP auto-picks host by free slots; FIFO queue when full | Caller picks host; resource-based; labels |
| 9 | Output = `git push` from inside the sandbox | Patch export with read-only clone |
| 10 | Idle timeout: any PTY byte either direction resets; 30 m default | Client-input-only; count preview traffic |
| 11 | Preview: `<port>-<sid>.preview.<domain>` + signed cookie | Path-based (breaks absolute paths); unauthenticated |
| 12 | Enrollment: daemon self-generates secret, CP sees fingerprint, pending + short code, admin approves | Join tokens; static shared secret; mTLS |
| 13 | SQLite only; daemon is source of truth for running state | Redis (solves nothing without multi-instance) |
| 14 | Raw Docker Engine API behind `SandboxDriver` | Sandcastle / TanStack sandbox (they want to own the agent run) |
| 15 | One default image in-repo, `image:` override per session | Caller-supplied always; per-host image |
| 19 | Agent is a per-session choice (`claude` \| `codex` \| `opencode` \| `shell`); one LLM gateway tuple (`base_url`, `api_key`, `model`) is mapped by the entry script onto each harness's own config | Claude-only; per-agent config schemas in the API |
| 16 | `Bun.serve` + `bun:sqlite`, zero runtime deps | Hono / Elysia |
| 17 | `/dev` xterm.js page behind `DEVAGENTS_DEV=true` | No UI; real Svelte frontend |
| 18 | One package, `src/{protocol,shared,cp,agent,dev}` | Bun workspaces |

## Topology

```
  host "hetzner-1"                     host "laptop" (NAT)
  ┌──────────────────────┐             ┌──────────────────────┐
  │ devagents             │             │ devagents             │
  │  ├ sandbox s_1 ─ pty │             │  └ sandbox s_3 ─ pty │
  │  └ sandbox s_2 ─ pty │             └──────────┬───────────┘
  └──────────┬───────────┘                        │
             │ wss (outbound, multiplexed)        │
             └────────────────┬───────────────────┘
                              ▼
                     ┌─────────────────┐
                     │  control plane  │  ◄── parent app (service token)
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
- **Creating:** CP sends `session.create` with repo, branch, prompt, image,
  idle timeout, and secrets. Daemon: create container → exec entry script in a
  PTY → `session.started`.
- **Running:** daemon owns the PTY regardless of viewers. Viewers attach via CP;
  attach = ring-buffer replay, then live. N viewers fan out; last resize wins.
- **Idle:** daemon tracks `last_activity` = last byte in *or* out of the PTY.
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

- **Parent → CP:** `Authorization: Bearer <DEVAGENTS_SERVICE_TOKEN>`. Every request
  carries `owner_id`; CP enforces `session.owner_id == owner_id`. CP has no
  user table.
- **Browser → terminal:** parent calls `POST /sessions/:id/attach-token`
  (60 s, single session). Browser opens `wss://cp/attach?token=…`.
- **Browser → preview:** parent calls `POST /sessions/:id/preview-token`
  (10 m). Browser hits `https://3000-s_x.preview.<domain>/?t=…`; CP verifies,
  sets a signed, subdomain-scoped cookie, redirects to `/`.
- **Secrets:** `secrets.git_token`, `secrets.anthropic_api_key`, `llm.api_key` on create.
  Forwarded to the daemon, injected as env into the container. Not written to
  SQLite, not logged, dropped from memory after `docker create`.
- **Enrollment:** daemon generates `host_secret` on first run
  (`~/.config/devagents/host.json`, 0600). `hello{name, fingerprint}` where
  `fingerprint = sha256(secret)`. Unknown fingerprint → CP inserts
  `status:pending`, mints code `XXXX-XX`, replies `pending{code}`; daemon
  prints it and waits. Admin: `POST /hosts/:id/approve {code}`. Next hello is
  accepted. Revoke = `status:revoked`. `DEVAGENTS_AUTO_APPROVE` does not exist in v1.
- **Git:** HTTPS clone; token supplied via `GIT_ASKPASS` helper. No SSH.

## Wire protocol (host ↔ CP)

One WebSocket. **Text frames** are JSON control messages. **Binary frames**
are `[u32 BE streamId][payload]`; a stream carries either PTY bytes for one
session or one proxied TCP connection for a preview port.

| direction | message |
|-----------|---------|
| host→cp | `hello{name, fingerprint, running: sid[], max_sessions}` |
| cp→host | `hello.ok{host_id}` · `hello.pending{code}` · `hello.rejected` |
| host→cp | `heartbeat{running, max}` every 10 s |
| cp→host | `session.create{sid, repo, branch, base_branch, prompt, image, idle_timeout_s, env}` |
| host→cp | `session.started{sid}` · `session.ended{sid, reason}` |
| cp→host | `session.destroy{sid}` |
| cp→host | `pty.open{sid, stream, cols, rows}` · `pty.resize{sid, cols, rows}` · `pty.close{stream}` |
| host→cp | `pty.replay{stream}` (binary follows) |
| cp→host | `port.dial{sid, port, stream}` · `port.close{stream}` |
| host→cp | `port.open{stream}` · `port.error{stream, msg}` · `port.close{stream}` |

Stream ids are allocated by the CP (odd) and never reused within a connection.

## HTTP API (parent → CP)

```
GET    /hosts                          list (status, online, running/max)
POST   /hosts/:id/approve   {code}
POST   /hosts/:id/revoke
POST   /sessions            {owner_id, repo, prompt, base_branch?, branch?, image?, idle_timeout_s?,
                             agent?: claude|codex|opencode|shell, model?,
                             llm?: {base_url?, api_key?},          // OpenAI/Anthropic-compatible gateway (LiteLLM)
                             secrets?: {git_token?, anthropic_api_key?}}
                            defaults: agent = DEVAGENTS_DEFAULT_AGENT (shell if prompt empty),
                                      model = DEVAGENTS_DEFAULT_MODEL (claude-sonnet-4-6), llm = DEVAGENTS_LLM_BASE_URL / DEVAGENTS_LLM_API_KEY (or plain LLM_BASE_URL / LLM_API_KEY)
GET    /sessions?owner_id=
GET    /sessions/:id
DELETE /sessions/:id
POST   /sessions/:id/attach-token      → {token, wss_url}
POST   /sessions/:id/preview-token {port} → {token, url}
GET    /attach?token=                   WebSocket (xterm ↔ pty)
*      <port>-<sid>.preview.<domain>/*  preview proxy
GET    /dev                             only with DEVAGENTS_DEV=true
```

Browser attach WS: binary frames = PTY bytes both ways; text frame
`{"type":"resize","cols","rows"}` from browser.

## Sandbox

`images/Dockerfile`: `debian:bookworm-slim` + git, curl, node, bun,
`@anthropic-ai/claude-code`, `@openai/codex`, `opencode-ai`; user `dev`; `WORKDIR /workspace`.
Entry (`images/entry.sh`, executed in the PTY): clone → `checkout -b $BRANCH` →
launch the chosen agent → always fall through to `bash` so the user can inspect
or `git push`. Empty prompt = `shell`. Gateway mapping (`LLM_BASE_URL` root, no `/v1`):

| agent | how the gateway is wired |
|-------|--------------------------|
| claude | `ANTHROPIC_BASE_URL=$BASE`, `ANTHROPIC_AUTH_TOKEN=$LLM_API_KEY`, `ANTHROPIC_MODEL=$MODEL` |
| codex | `~/.codex/config.toml` with `model_providers.gateway.base_url=$BASE/v1`, `env_key=OPENAI_API_KEY`, approvals off |
| opencode | `~/.config/opencode/opencode.json` provider `gateway` (`@ai-sdk/openai-compatible`, `$BASE/v1`), `model=gateway/$MODEL` | Container: no privileged, default bridge network, container
IP used for preview dialing.

`SandboxDriver` interface (only `docker` implemented):

```ts
create(opts): Promise<SandboxId>
attach(id, cmd, size): Promise<PtyStream>   // docker exec Tty:true, hijacked
resize(id, execId, size): Promise<void>
dial(id, port): Promise<Duplex>             // TCP to container ip:port
destroy(id): Promise<void>
```

## Storage (SQLite)

```
hosts    (id, name, fingerprint UNIQUE, status pending|approved|revoked,
          approve_code, max_sessions, last_seen_at, created_at)
sessions (id, owner_id, host_id NULL, status, ended_reason NULL, ended_detail NULL,
          repo, branch, base_branch, prompt, image, idle_timeout_s,
          agent, model NULL, llm_base_url NULL,
          created_at, started_at, ended_at, unknown_since NULL)
```

No terminal bytes. No secrets. Queue position is derived from `created_at`
among `queued` rows. Because secrets are never persisted, `queued` sessions cannot
survive a CP restart: on boot they are marked `ended/failed` and must be recreated.

## Repo layout

```
src/protocol/   messages.ts  framing.ts        shared types + binary framing
src/shared/     ids.ts  errors.ts  log.ts
src/cp/         main.ts  http.ts  tunnel.ts  scheduler.ts  store.ts
                attach.ts  preview.ts  tokens.ts
src/agent/      main.ts  config.ts  tunnel.ts  docker.ts  pty.ts  sessions.ts
src/dev/        index.html
images/         Dockerfile  entry.sh
```

Scripts: `bun run cp`, `bun run agent`, `bun test`.

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
11. mTLS; join tokens; auto-approve
12. Any real frontend
