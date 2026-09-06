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
| 16  | Go for the two daemons (revised 2026-09-04, see `PLAN.md`): `cmd/sandboxd-api` and `cmd/sandboxd-worker`, one static binary each, stdlib first. `net/http` + `httputil.ReverseProxy` own the sockets and the preview proxy, `database/sql` + a cgo-free SQLite driver own the state behind consumer-declared interfaces, huma renders the OpenAPI document from the handler types. Bun stays for the reference client only (`examples/ui`). Cut over 2026-09-05: the TypeScript control plane, worker and `packages/core` are deleted, and the repo root is a Go module with no `package.json`. The wire protocol, the HTTP API, the tokens and the SQLite schema did not change. Revised 2026-09-06: huma is replaced by `ogen`, and the direction reverses — the document is written and the Go server is generated from it (decision 22). `paths/client`, `ogen/otel` and `ogen/unimplemented` are disabled and ogen is pinned as a `tool` in `go.mod`, so `go generate ./...` needs nothing installed and no OpenTelemetry is linked. Generated validation replaces huma's, and its `validate.Error{Fields}` replaces recovering a field name from huma's prose with a regex. Measured after the cutover, and worse than expected on one axis: the API binary goes from 10 linked module roots to 27, because the generated handlers import `ogenerrors`, which reaches `ogen/openapi` → `jsonschema` → `location` — the *document parser's* diagnostics, and with them zap, goldmark and two YAML loaders. It is not reachable through a feature flag. The size did not follow the count: 18.1 MB before, 17.8 MB after, because huma weighed more per root than what replaced it | Bun for everything (2026-09-03: `Bun.serve`, `bun:sqlite`, Hono — hand-rolled HTTP and WebSocket framing over tunnel streams, 100 MB JIT binaries, no Kubernetes client); Elysia; multi-runtime targets (Workers, Lambda): the CP is a stateful single instance; Rust (ceremony the size does not need); Go for the UI too (a YAML loader and one HTML page); `oapi-codegen` (it does not validate a request body, so `kin-openapi` middleware comes back and with it the error-prose adapter huma forced); staying on huma (~120 of the 630 lines in `internal/cp/http` existed to correct its output: `$schema`, array nullability, nullable enums, and a field name lifted out of a validation sentence); `swaggo/swag` (comment annotations, no compile-time link between handler and document); ogen with its defaults (the client and the OpenTelemetry integration are 4,500 generated lines and three modules we have no use for) |
| 17  | `examples/ui` (revised 2026-09-05): a Bun + Hono app that serves the xterm.js page at `/`, holds the service token, resolves presets, and forwards everything else to the API. The parent-app stand-in, and after the cutover a genuine external client — its own `package.json`, `tsconfig.json` and linter config, importing nothing from this repo but the generated SDK (amended 2026-09-06, decision 22). Revised 2026-09-06: one page per concern — `/hosts`, `/sandboxes`, `/sandboxes/new`, `/sandboxes/:id` — each an html file plus the TypeScript it references, bundled by `Bun.serve` routes and typed against the SDK's types; the JSON lives under `/api`, so a page URL and its data never collide. xterm.js comes from npm, not a CDN. Same day: the byte proxy is gone — every `/api` route is one generated SDK call, so a route the SDK cannot express is a gap in the document, caught here. | No UI; real Svelte frontend; a `/dev` page inside the control plane; one page holding hosts, the form and the terminal (it grew to 340 lines of untyped script); a client-side router or framework for four pages |
| 18  | One Go module `sandboxd` at the root (revised 2026-09-04): `cmd/sandboxd-api`, `cmd/sandboxd-worker`, `internal/{wire,cp,worker}`; `depguard` in `.golangci.yml` enforces cp → wire, worker → wire, never each other, `wire` a leaf, and one tree per `cmd`; the dependency list in `PLAN.md` is closed. The control-plane tree is `cp`, not `api`: the binary is the API, the tree is the whole daemon, and `internal/api/httpapi` stutters. `cp` never imports its own subpackages either: it declares the interfaces it needs and `cmd/sandboxd-api` supplies them, which is what keeps the tree acyclic without a wiring framework. Cut over 2026-09-05: `apps/api`, `apps/worker` and `packages/core` are gone, the root has no `package.json` or workspace, and `examples/ui` is a standalone Bun app with its own tooling. Revised 2026-09-06: `internal/openapi` joins `wire` as a leaf, because `internal/cp` consumes the generated request and response types and may not import its own subpackages — the generated package therefore cannot live under `internal/cp/http` | Bun workspaces for everything (2026-09-03: `apps/api`, `apps/ui`, `apps/worker`, `packages/core`, fallow as the boundary); one package, `src/{protocol,shared,cp,agent,dev}`; `internal/` alone as the boundary (it only blocks imports from outside the module, not between our own trees); `internal/api` for the control plane (two meanings for one word, and a stuttering HTTP package inside it); generating into `internal/cp/http/gen` (`internal/cp` would then import its own subpackage, which is the one rule that keeps this tree acyclic without a wiring framework) |
| 20  | Generic core (`image, cmd, env, secret_env`) + presets as a declarative field→env schema; the image is the plugin, its entry script owns the semantics. The generic half is the API, the preset half is the UI (revised 2026-09-03)                                                                                                                                                                                                                                                                                                            | Agent kinds baked into the protocol and daemon; driver plugins in the daemon; per-preset code on the CP                                                                                                                                                                                                                                                      |
| 21  | A sandbox is exactly one container, from one Dockerfile (revised 2026-09-04). No sidecars, no per-sandbox network, no readiness probes: none of the presets needed them, and the compose translator, the catalog and the pod lifecycle were the largest part of the code for no user. A repo that needs a database runs it inside the sandbox or points at one outside                                                                                                                                                                         | Sidecar `services` from a compose document plus a catalog (`services.yaml`), added 2026-09-03 and removed here; compose as the runtime (`docker compose up` per session); CP reading `.sandboxd.yaml` from the repo                                                                                                                                          |
| 22  | The OpenAPI document is a checked-in artifact, `openapi.json` at the root, written by `go run ./scripts/openapi` from the same handler registration the server runs; a Go test fails when it is stale. `sdk/typescript` (`@sandboxd/sdk`) is generated from it by hey-api, its output checked in beside a hand-written entry point that owns the public surface. This amends decision 17: `examples/ui` now depends on one package from this repo, the generated SDK, and on nothing else — it is the proof that the document is usable, which a client that hand-writes its types is not (added 2026-09-06). Revised 2026-09-06, same day: reversed. `api/client.yaml` is the source, hand-written (and `api/admin.yaml` beside it, decision 27); `internal/openapi` (ogen) and `sdk/typescript/src/generated` (hey-api) are both output, both checked in, and CI regenerates each and fails on a diff. `scripts/openapi` and `internal/cp/http/spec.go` are deleted along with the direction they served. YAML rather than JSON because the document now carries the rationale that used to live in the Go struct tags. `GET /sandboxes/{id}/terminal` stays in the document and out of the generated router: it is a WebSocket upgrade with no `ResponseWriter` to hand a handler, so `net/http` takes the route before ogen sees it. `GET /tunnel` stays out of the document entirely — the worker speaks the wire protocol below, not this API. `api/` is also a Go package: it embeds both documents so the control plane serves the exact bytes it was generated from, rather than a rendering of them that could disagree | A hand-written client per language; generating from a running control plane (a generator would need a booted server and a token); publishing the SDK from a separate repo; leaving `examples/ui` on hand-written types (its `CreateSandbox` had already drifted from a comment saying it should be generated); keeping the document an artifact of the handlers (every one of the generator's opinions had to be corrected afterwards, and a hand-written path item for `/attach` was Go that built a `huma.PathItem`); a second document for `/attach` in AsyncAPI (one socket does not earn a second contract) |
| 23  | Every list and map in a response is empty rather than null, and a nullable field carrying an enum lists `null` among its values (added 2026-09-06). A nil Go slice marshals as `null`, so huma marked every array nullable and every generated field `T[] \| null` — while `GET /sandboxes` was already returning `[]`. `HostView.capacity` is the one field that is absent rather than null: huma refuses to type a nullable object reference, and `online` already says whether there is a number to read | Leaving the document as huma renders it (a generated client unwraps a null the API never sends, and reads `ended_reason` as always present); `omitempty` everywhere (an absent key and a null are different to a caller, per `internal/cp/api.go`)                                                                       |
| 24  | `owner_id` is a header, `X-Sandboxd-Owner`, on every route that scopes to one owner, and it is required (added 2026-09-06). It travelled four ways at once: a field in the `POST /sandboxes` body, a query parameter on `GET`/`DELETE /sandboxes/{id}`, a request body on both token mints — a `POST` body whose only field was the owner — and absent from `GET /sandboxes`, where absence meant every owner's sandboxes. It is a credential that narrows the service token, not payload, so it belongs beside the token. There is no unscoped listing at all: `GET /sandboxes` is this owner's, and the fleet-wide view is not a route a parent app can reach by omitting something. Breaking, and taken deliberately while the document was rewritten | Leaving it in three places (a generated client shows the inconsistency to every caller); a query parameter everywhere (it is a credential, and query strings are logged); keeping the unscoped list as the no-parameter default (the failure mode is a parent app enumerating the fleet, which is the worst possible default); a separate `GET /sandboxes/all` (a second route, and a second thing to secure, for a view nothing has asked for yet) |
| 25  | A mutation with nothing to say answers `204` (added 2026-09-06). `POST /hosts/{id}/approve` and `POST /hosts/{id}/revoke` returned `{"ok": true}` typed as `enum: [true]` — a body that carries one bit, and that bit is the status code. The `Ok` schema goes with them. `GET /healthz` keeps a body, because a probe is the one place worth having room to say more than "up" later | `200 {ok:true}` (a generated client unwraps a field that can only hold one value); `DELETE /hosts/{id}` for revoke (a revoked host is a row we keep, not one we remove — it is rejected on its next hello) |
| 26  | A minted capability is a URL, not a token (added 2026-09-06). `POST /sandboxes/{id}/terminal` and `POST /sandboxes/{id}/preview` each answer with one `Link` — `{url, expires_in_s}` — and the token rides inside the URL. The caller's only move was ever to hand the URL to a browser: the reference client destructured `wss_url` and `url` and ignored `token` and the rest, which is what a field nobody reads looks like. The terminal is one path with two methods, `POST` to get the link and `GET` to be upgraded on it, so the resource and the socket share a name; the browser's `GET` carries neither bearer nor owner header, because a WebSocket cannot send one and the token in the query is the whole gate. Preview still names its port in the body — a sandbox has many, and the URL is per-port | Returning `{token, wss_url, expires_in_s}` and `{token, url, expires_in_s}` (two schemas and three fields for one string that clients used); a top-level `GET /attach?token=` (the sandbox is already in the path everywhere else, and the socket belongs beside the resource it opens); folding the mint into `SandboxView` (a capability in every list response, minted on every read) |
| 27  | Two documents, and only one of them is a promise (added 2026-09-06). `api/client.yaml` is the client contract — sandboxes, the terminal, previews, the probe — and `sdk/typescript` is generated from it, so it changes carefully. `api/admin.yaml` is the operator surface — enrolling workers and looking at the fleet — with no published client and no stability claim; it breaks whenever the operator is better served. They generate into `internal/openapi` and `internal/adminapi`, two leaves beside `wire`, and mount on the same listener behind the same `Bearer` today. `ErrorModel` and `Issue` are duplicated rather than `$ref`'d across files, so neither document needs the other to resolve and neither generator reads two: the shapes must agree because one Go error mapping serves both, but that is a fact about the code, not a dependency between contracts. Giving the operator its own credential is now a change to one scheme in one file | One document with tags (a published SDK then carries `approveHost` and `revokeHost`, and every fleet change is a breaking change to a parent app's client); filtering one rendered document by tag (the served document and the checked-in one then describe different route sets); a shared `api/common.yaml` (it re-couples the two contracts the split exists to separate, for twenty lines); generating an admin TypeScript client into `sdk/typescript` (publishing it is the stability claim we are declining to make — `examples/ui` generates its own locally) |

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

- **Parent → CP:** `Authorization: Bearer <SANDBOXD_SERVICE_TOKEN>`, and
  `X-Sandboxd-Owner` beside it on every owner-scoped route (decision 24); CP enforces
  `sandbox.owner_id == the header`. CP has no user table. Both `/sandboxes` and `/hosts`
  answer to the one token today: splitting the operator surface onto its own credential is
  a second token and a switch in one `SecurityHandler`, not a new middleware.
- **Browser → terminal:** parent calls `POST /sandboxes/:id/terminal` (60 s, single
  sandbox) and gets back a URL with the token already in it. Browser opens
  `wss://cp/sandboxes/:id/terminal?token=…`; the token is the whole gate there.
- **Browser → preview:** parent calls `POST /sandboxes/:id/preview` with a port
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
page live in `examples/ui`, the reference client, which turns a friendly request into this.

`api/client.yaml` is the contract and it is written, not rendered: `internal/openapi` is
ogen's server and types generated from it, `sdk/typescript` is hey-api's client generated
from the same file, and CI regenerates both and fails on a diff (decision 22). Validation is
generated too, so a constraint is stated once, in the document. It is served at
`GET /openapi.yaml`, the operator's at `GET /openapi.admin.yaml`, and both are rendered at
`GET /doc`. The bytes served are the files themselves — a small `api` package embeds them —
so the served document and the checked-in one cannot disagree. The operator's routes are a second document,
`api/admin.yaml` → `internal/adminapi`, with no published client and no stability claim
(decision 27). Errors are `{error, issues?}`; a validation
failure lists every failing field. Unknown body keys are a 400, so a `repo` or `preset` sent
here says the caller meant the UI.

`X-Sandboxd-Owner` carries the owner on every route that belongs to one, and is required on
each (decision 24): it narrows the service token rather than describing the request, so it
travels beside the token. A sandbox owned by somebody else is indistinguishable from a
missing one. Response invariants a generated client is typed on: every list and map is
present and empty rather than null, `ended_reason` includes `null` in its enum, and
`capacity` is absent while a host is offline (decision 23).

```
client (api/client.yaml — the contract; a published SDK comes from this)
GET    /healthz  /openapi.yaml  /openapi.admin.yaml  /doc   public
POST   /sandboxes           {image, cmd?: string[], env?, secret_env?, idle_timeout_s?, tags?: string[]}
                            tags: every one must be present on a host for it to be a candidate.
                            422 when no approved host carries them all; queues when they are just full.
                            TERM and SANDBOXD_SESSION_ID are reserved env names.
                            Operator defaults for every sandbox: SANDBOXD_SANDBOX_ENV_<NAME>=value,
                            shorthands SANDBOXD_LLM_BASE_URL / SANDBOXD_LLM_API_KEY (or LLM_BASE_URL / LLM_API_KEY).
                            Precedence: secret_env > env > operator > entry.sh default.
GET    /sandboxes                       this owner's, newest first; there is no unscoped list
GET    /sandboxes/:id
DELETE /sandboxes/:id
POST   /sandboxes/:id/terminal               → {url, expires_in_s}   wss://, 60 s
GET    /sandboxes/:id/terminal?token=   the socket itself (xterm ↔ pty). No bearer, no owner
                                        header: a WebSocket cannot send one and the token is
                                        the gate. In the document, out of the generated
                                        router — an upgrade has no ResponseWriter to hand a
                                        handler, so net/http takes the route first.
POST   /sandboxes/:id/preview  {port}        → {url, expires_in_s}   https://, 10 min

admin (api/admin.yaml — operator surface; no published client, breaks freely)
GET    /hosts                           list (status, online, running/max, tags)
POST   /hosts/:id/approve   {code}      → 204
POST   /hosts/:id/revoke                → 204

neither document
GET    /tunnel                          worker only: it speaks the wire protocol above,
                                        not this API
*      <port>-<sid>.preview.<domain>/*  preview proxy (HTTP + WebSocket)
```

## UI (parent-app stand-in, `examples/ui`)

Holds the service token and the presets (`examples/ui/presets/<name>/preset.yaml`). Serves
one page per concern — `/hosts`, `/sandboxes`, `/sandboxes/new`, `/sandboxes/:id` (the
terminal); `/` redirects to the list — and the JSON under `/api`, one generated
`@sandboxd/sdk` call per route, no byte proxy. The browser never holds the token. It sends
the owner as `X-Sandboxd-Owner` on every call; the header is required by the document, so
the generated client asks for it rather than the API answering 400 (decision 24).

`/api/hosts` is the exception to "one SDK call per route": the fleet is not in the published
SDK (decision 27), so `examples/ui` generates its own types from `api/admin.yaml` into its
own tree. That is the arrangement working as intended — the operator surface may break here
without breaking a parent app — and it is the price of the split, paid by the one client
that wants both halves.

```
GET    /api/presets                     [{name, description, image, cmd, idle_timeout_s, preview: {port, port_field},
                                          fields: [{name, type, env, required, secret, default?, values?, min?, max?, multiline?, description?}]}]
POST   /api/sandboxes       {owner_id, preset?, image?, cmd?, env?, secret_env?, idle_timeout_s?, …preset fields}
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
GET    /api/hosts   POST /api/hosts/:id/{approve,revoke}   GET|DELETE /api/sandboxes/:id
                            the three host routes are generated from api/admin.yaml here, not from the SDK
POST   /api/sandboxes/:id/{terminal,preview}   openTerminal and openPreview; the Link relayed as is
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
api/                    client.yaml  admin.yaml  ogen.yml  api.go      the contracts, and the package that embeds them
internal/wire/          messages.go  framing.go  ids.go  + testdata/    the wire contract; a leaf
internal/openapi/       oas_*_gen.go                            ogen, from api/client.yaml; a leaf, never edited
internal/adminapi/      oas_*_gen.go                            ogen, from api/admin.yaml; a leaf, never edited
internal/cp/            errors.go  config.go  env.go  tokens.go  capacity.go  scheduler.go  sandboxes.go
internal/cp/store/      model.go  sqlite.go                     the only package that speaks SQL
internal/cp/hosts/      conn.go  stream.go  hub.go  enrollment.go  service.go   one Conn per worker; Stream is a net.Conn
internal/cp/attach/     bridge.go                                browser terminal <-> PTY
internal/cp/preview/    proxy.go                                 ReverseProxy over a tunnel stream
internal/cp/http/       routes.go  auth.go  sandbox.go  host.go  errors.go    the two Handlers; the only place a status code lives
internal/worker/        config.go  ring.go  sandboxes.go  tunnel.go
internal/worker/driver/ driver.go  docker.go                    (kubernetes.go next)
cmd/sandboxd-api/      main.go                                  wiring only
cmd/sandboxd-worker/   main.go                                  wiring only
scripts/dev/           main.go                                  the whole stack in one terminal; not shipped
sdk/typescript/        openapi-ts.config.ts  src/index.ts       @sandboxd/sdk; the hand-written entry point
sdk/typescript/src/generated/                                   hey-api output; regenerated, never edited
examples/ui/src/       main.ts  config.ts  api.ts  sandboxes.ts  errors.ts  json.ts  log.ts
examples/ui/src/pages/ hosts  sandboxes  new  sandbox (.html + .ts each)   one page per concern, bundled by Bun
examples/ui/src/pages/ shell.ts  client.ts  format.ts  term.ts  style.css   what the pages share
examples/ui/src/presets/ index.ts  types.ts  schema.ts  preset.ts  loader.ts   yaml -> Preset; registry
examples/ui/presets/   build.ts                                 one image per preset with a Dockerfile
examples/ui/presets/<name>/ preset.yaml  Dockerfile  entry.sh  …   coding-agent  vscode  jupyter; ubuntu  python  node  http  notebook (stock images); custom (no image)
<pkg>_test.go          beside the source it tests; examples/ui/test/ for the client
```

Commands: `go build ./...`, `go test ./...`, `golangci-lint run`, `go run ./scripts/dev`,
`nix build .#api`. In `examples/ui`: `bun run dev`, `bun run image [preset…]`, `bun test`.
After editing either document in `api/`: `go generate ./...` for the Go half, and
`bun run generate` in `sdk/typescript` when `client.yaml` changed. CI runs both and fails on
a diff, so the generated trees can only be current. `admin.yaml` has no SDK step — that is
the point of it.

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
