- ALWAYS USE PARALLEL TOOLS WHEN APPLICABLE.
- The working branch is `dev`; `main` is the release branch.
- `DESIGN.md` is the signed v1 contract. A behaviour change is a decision-table edit
  there first, then code.

## Stack

**This is a Go repository.** One module, `sandboxd`, at the root: two daemons, one wire
contract, one dev runner. The only TypeScript left is `examples/ui`, the reference client,
which is a self-contained Bun app that imports nothing from here (`DESIGN.md` decisions
16, 17 and 18).

| Path                    | What                                                                             |
| ----------------------- | -------------------------------------------------------------------------------- |
| `internal/wire`         | The wire contract: messages, framing, ids. A leaf.                               |
| `internal/cp`           | The control plane: config, tokens, scheduler, sandbox use cases                  |
| `internal/cp/store`     | SQLite. The only package that speaks SQL.                                        |
| `internal/cp/hosts`     | Worker tunnels: enrollment, the hub, and a stream that is a `net.Conn`           |
| `internal/cp/attach`    | Browser terminal ↔ PTY                                                           |
| `internal/cp/preview`   | `<port>-<sid>.<domain>` → a port inside a sandbox, over `httputil.ReverseProxy`  |
| `internal/cp/http`      | huma: routes, validation, the OpenAPI document. The only place a status code lives |
| `internal/worker`       | The per-host daemon: driver, PTYs, one outbound tunnel                           |
| `internal/worker/driver`| Docker today (moby client); Kubernetes next                                      |
| `cmd/sandboxd-api`      | Wiring only, one binary                                                          |
| `cmd/sandboxd-worker`   | Wiring only, one binary                                                          |
| `scripts/dev`           | `go run ./scripts/dev` — the whole stack in one terminal. Not shipped.           |
| `examples/ui`           | The reference client: service token, presets, the xterm.js page. Its own `AGENTS.md`. |
| `examples/ui/presets`   | Data. One folder per preset with its Dockerfile. Built by `bun run image` there.  |

`cp` and `worker` only meet on the wire: both import `wire`, neither imports the other,
and `wire` imports nothing of ours. `cp` never imports its own subpackages — it declares
the interfaces it needs and `cmd/sandboxd-api` supplies them. Declared in `.golangci.yml`
as `depguard` rules, enforced by `golangci-lint`.

## Commands

| Command                        | What                                                            |
| ------------------------------ | --------------------------------------------------------------- |
| `go build ./...`               | Both binaries and the dev runner                                |
| `go test ./...`                | Every Go package; CI adds `-race`                               |
| `go vet ./...`                 |                                                                 |
| `golangci-lint run`            | Lint, including the `depguard` import boundary                  |
| `golangci-lint fmt`            | `gofumpt`, run through the linter so the version is pinned once |
| `go run ./scripts/dev`         | API, worker and UI together [DO NOT RUN unless the user asks]   |
| `go run ./cmd/sandboxd-api`    | Control plane [DO NOT RUN unless the user asks]                 |
| `go run ./cmd/sandboxd-worker` | A worker on this machine [DO NOT RUN unless the user asks]      |
| `nix build .#api` / `.#worker` | One static binary each; `--system aarch64-linux` cross-compiles |

The live Docker check is env-guarded and needs an Engine:
`SANDBOXD_DOCKER_TEST=1 go test ./internal/worker/driver/`.

Neither toolchain is on `PATH` outside the devShell: `nix develop --command <cmd>`, or
`direnv allow` once. `.zed/` points Zed at the same devShell.

Before handing work back:
`golangci-lint fmt && go vet ./... && golangci-lint run && go test ./...`.

**That is the gate.** `examples/ui` has its own, in its own `AGENTS.md`, and CI runs both.

## Go

Stdlib first. The dependency table in `PLAN.md` is **closed** — adding a module is a
`DESIGN.md` row, not a judgement call.

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
  **only `cp/http` knows a status code.** An HTTP status raised inside a use case is the
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
