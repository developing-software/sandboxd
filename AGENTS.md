- ALWAYS USE PARALLEL TOOLS WHEN APPLICABLE.
- The working branch is `dev`; `main` is the release branch.
- `VISION.md` is what this is and is not; permanent. `PLAN.md` is what is being built
  next; temporary. Decisions and what they rejected are ADRs under `docs/adr/` (being
  written; code comments still cite them as `DESIGN.md decision N` until then). A
  behaviour change is an ADR first, then code.

## Stack

**This is a Go repository.** One module, `sandboxd`, at the root: two daemons, one wire
contract, two dev runners. The two HTTP contracts in `api/` are written by hand and
everything else about them is generated — ogen's servers for Go, hey-api's client for
`sdk/typescript` — and `examples/ui` is the reference client, a Bun app that imports
nothing from here but that SDK.

| Path                    | What                                                                              |
| ----------------------- | --------------------------------------------------------------------------------- |
| `api`                   | `client.yaml` and `admin.yaml`, hand-written. The source both generators read.     |
| `internal/wire`         | The wire contract: messages, framing, ids. A leaf.                                |
| `internal/conf`         | The config file loader: strict YAML, `${VAR}`, `_file` twins. A leaf.             |
| `internal/gen`          | Generated, never edited, a leaf: `clientapi` from `api/client.yaml`, `adminapi` from `api/admin.yaml`, both ogen |
| `internal/sandbox`      | `Driver`, `PTY`, the `Manager` with its fanout and ring, `Tags`. Imports `wire` only; both daemons import it |
| `internal/sandbox/docker` | The Docker driver (moby client) and its `Config`, one block in either daemon's file |
| `internal/sandbox/drivers` | The registry: the `driver:` / `providers.<name>` block decoded, validated and opened. The only place a driver's name meets its package |
| `internal/sandbox/sandboxtest` | The fake driver every manager owner tests against                           |
| `internal/cp`           | The control plane: config, tokens, scheduler and its placement policy, sandbox use cases |
| `internal/cp/store`     | SQLite. The only package that speaks SQL.                                         |
| `internal/cp/hosts`     | The `workers` provider: enrollment, the hub, and a stream that is a `net.Conn`    |
| `internal/cp/local`     | A provider in-process: one `sandbox.Manager` over any driver, shown as one host   |
| `internal/cp/fleet`     | Routes a host id to whichever provider owns it, behind `cp`'s interfaces          |
| `internal/cp/attach`    | Browser terminal ↔ PTY, behind its own `PTY` interface                            |
| `internal/cp/preview`   | `<port>-<sid>.<domain>` → a port inside a sandbox, over `httputil.ReverseProxy`   |
| `internal/cp/server`    | The two generated `Handler`s, the bearer check, and the only place a status lives  |
| `internal/worker`       | The per-host daemon: config, one outbound tunnel (`tunnel`, `session`, `port`) — the remote face of a `sandbox.Manager` |
| `cmd/sandboxd-api`      | Wiring only, one binary                                                           |
| `cmd/sandboxd-worker`   | Wiring only, one binary                                                           |
| `scripts/dev`           | `go run ./scripts/dev` — the whole stack in one terminal. Not shipped.            |
| `sdk/typescript`        | `@sandboxd/sdk`. `src/generated` is hey-api's output; `src/index.ts` is ours.     |
| `examples/ui`           | The reference client: service token, presets, the xterm.js page. Its own `AGENTS.md`. |
| `examples/ui/presets`   | Data. One `preset.yaml` per preset, naming an image in `images/`. Builds nothing.  |
| `images`                | The official sandbox images: `agent`, `vscode`, `jupyter`. Published to GHCR by CI. |
| `images/agent/agents`   | One `.sh` per coding agent, discovered by the entry. Its `README.md` is the contract. |

`cp` and `worker` only meet on the wire: both import `wire`, neither imports the other,
and `wire` imports nothing of ours. `sandbox` is shared code, not a channel between them:
it imports `wire` only, and `depguard` denies it both daemons. `conf` is a leaf. `cp` never
imports its own subpackages — it declares the interfaces it needs and `cmd/sandboxd-api`
supplies them, and it consumes the generated request and response types directly, which is
why `internal/gen` is a leaf beside `wire` rather than a package under `cp/server`.
`fleet` is denied `hosts` and `local`, which plug into it, and `local` is denied `hosts`.
Declared in `.golangci.yml` as `depguard` rules, enforced by `golangci-lint`.

Each daemon reads one YAML file — `--config`, `SANDBOXD_CONFIG`, then
`/etc/sandboxd/{api,worker}.yaml` — and falls back to the legacy `SANDBOXD_*` variables
when none exists (ADR 28). `--check-config` prints the effective configuration, redacted.
A **driver** is what runs a sandbox (`sandbox/docker`); a **provider** is where the manager
runs: behind the tunnel (`providers.workers`, `cp/hosts`) or in-process
(`providers.docker`, `cp/local` over that driver). Callers select with tags —
`provider:workers`, `provider:local` — and the API does not change (ADR 30).

## Commands

| Command                        | What                                                            |
| ------------------------------ | --------------------------------------------------------------- |
| `go build ./...`               | Both binaries and the dev runner                                |
| `go test ./...`                | Every Go package; CI adds `-race`                               |
| `go vet ./...`                 |                                                                 |
| `golangci-lint run`            | Lint, including the `depguard` import boundary                  |
| `golangci-lint fmt`            | `gofumpt`, run through the linter so the version is pinned once |
| `go generate ./...`           | Regenerate `internal/gen` from `api/`                             |
| `go run ./scripts/dev`         | API, worker and UI together [DO NOT RUN unless the user asks]   |
| `go run ./cmd/sandboxd-api`    | Control plane [DO NOT RUN unless the user asks]                 |
| `go run ./cmd/sandboxd-worker` | A worker on this machine [DO NOT RUN unless the user asks]      |
| `nix build .#api` / `.#worker` | One static binary each; `--system aarch64-linux` cross-compiles |

The live Docker checks are env-guarded and need an Engine:
`SANDBOXD_DOCKER_TEST=1 go test ./internal/sandbox/docker/ ./internal/cp/local/`.

Neither toolchain is on `PATH` outside the devShell: `nix develop --command <cmd>`, or
`direnv allow` once. `.zed/` points Zed at the same devShell.

Before handing work back:
`golangci-lint fmt && go vet ./... && golangci-lint run && go test ./...`.

**That is the gate.** `sdk/typescript` and `examples/ui` have their own; CI runs all three.

A change to a request or response type starts in `api/client.yaml` — the document leads, the
code follows. Then `go generate ./...` for the Go half, and `bun run generate` in
`sdk/typescript` for the TypeScript one. Both trees are checked in and CI regenerates each
and fails on a diff, so stopping after the first step is caught. `api/admin.yaml` has no SDK
step: the operator surface has no published client, which is what lets it break (decision 27).

## Go

Stdlib first. The direct dependencies in `go.mod` are a **closed** list — adding a module
is an ADR, not a judgement call.

- **A package is a noun, a function is a verb.** `wire.Decode`, `store.SQLite`,
  `hosts.Hub`. Never `wire.DecodeWireMessage`: the package name is already in the call.
- **The consumer declares the interface, the producer exports a struct.** `store.SQLite`
  is concrete and exported; `scheduler.go` declares the ten-method interface it needs and
  `sandboxes.go` a four-method one. A single 20-method `Store` would port the god object.
- **Interfaces exist so a package does not depend on SQLite, not so a test can fake it.**
  The CP suite drives the real store on `:memory:`; a `fakeStore` is the banned mock of
  our own module. Fakes are for boundaries we do not own: the driver, the transport.
- Errors wrap with `%w` and are compared with `errors.Is`. Use cases return the sentinels
  in `cp/errors.go` (`ErrNotFound`, `ErrConflict`, `ErrInvalid`, `ErrUnsatisfiable`);
  **only `cp/server` knows a status code.** An HTTP status raised inside a use case is the
  bug that rule exists to prevent.
- No `any` in an exported signature. `json.RawMessage` at the wire boundary is not `any`.
- Everything long-lived takes a `context.Context` as its first parameter.
- **One writer goroutine per WebSocket**, fed by a buffered channel; nothing else calls
  `Write`. Closing the channel closes the socket.
- A full writer channel is a decision, per stream kind, written down where it happens:
  a PTY viewer is **dropped** (the ring replays on reattach), a port stream **blocks**
  (that is the TCP backpressure the `net.Conn` promises).
- **The scheduler goroutine is the only writer of sandbox state**, and that includes the
  insert: a row a handler wrote would be visible to the next drain before its secrets
  were, and would be placed twice. API calls reach the loop as events on the same channel
  the hub uses, so a caller cannot observe them out of order.
- No lock is held while sending on a socket or calling into another struct. Copy out,
  unlock, then act.
- Comments state the non-obvious constraint — why, not what. Below three lines.
- `.golangci.yml` disables a rule only with a written reason.

## Testing

- `_test.go` beside the source it tests.
- **No mocks of our own modules**: the CP suite drives the real `store.SQLite` on
  `:memory:` and the real scheduler against a fake placement. A fake is allowed where the
  boundary is someone else's — the container driver, the tunnel transport, a sandbox's
  own HTTP server.
- Secrets must never appear in a SQLite row: tests assert on the raw table with
  `store.Dump`, which exists for exactly that. Keep it.
- `httptest.Server` for the API and for a real WebSocket against the real hub;
  `net.Conn` end to end for the preview proxy.
- Race-sensitive behaviour gets a test that fails without the fix, run under
  `go test -race -count=N`. The double-placement and the double-close both had one.
