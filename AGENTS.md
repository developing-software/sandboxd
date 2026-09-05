- ALWAYS USE PARALLEL TOOLS WHEN APPLICABLE.
- The working branch is `dev`; `main` is the release branch.
- `DESIGN.md` is the signed v1 contract. A behaviour change is a decision-table edit
  there first, then code.

## Stack

Bun workspaces monorepo. Bun is the only runtime: one entrypoint per app, no bundling
targets.

| Path              | Package            | What                                                                                    |
| ----------------- | ------------------ | --------------------------------------------------------------------------------------- |
| `packages/core`   | `@sandboxd/core`   | The wire contract: messages, framing, ids, log. A leaf.                                 |
| `apps/api`        | `@sandboxd/api`    | The control plane: a generic JSON API (Hono + Zod), host tunnel, attach + preview proxy |
| `apps/ui`         | `@sandboxd/ui`     | The parent-app stand-in: service token, presets, the xterm.js page                      |
| `apps/worker`     | `@sandboxd/worker` | The per-host daemon: Docker driver, PTYs, one outbound tunnel                           |
| `apps/ui/presets` | —                  | Data. One folder per preset with its Dockerfile. Built by `bun run image`               |

Each package has its own `AGENTS.md` (symlinked as `CLAUDE.md`) with the rules for
editing it.

`api` and `worker` only meet on the wire: both import `core`, neither imports the other.
`ui` sits in front of `api` and may import its types (the routes, for `hono/client`; the
schema, for what it sends) — never the reverse. `core` imports nothing. Declared in `.fallowrc.jsonc`, enforced by `bun run fallow`.

## Commands

| Command              | What                                                                                        |
| -------------------- | ------------------------------------------------------------------------------------------- |
| `bun run dev:api`    | Control plane on :8080, state in `.data/`, docs at `/doc` [DO NOT RUN unless the user asks] |
| `bun run dev:ui`     | UI on :8081, talking to the local API [DO NOT RUN unless the user asks]                     |
| `bun run dev:worker` | A worker on this machine, dialling the local CP [DO NOT RUN unless the user asks]           |
| `bun run image`      | One Docker image per preset with a Dockerfile                                               |
| `bun run test`       | Every package                                                                               |
| `bun run typecheck`  | Every package                                                                               |
| `bun run fmt`        | `oxfmt`, not Prettier                                                                       |
| `bun run lint`       | `oxlint`, not ESLint                                                                        |
| `bun run fallow`     | Dead code, duplication, complexity, import boundaries                                       |

Before handing work back: `bun run fmt && bun run lint && bun run typecheck && bun run test`.
`bun run fallow` still reports inherited dead code; do not add to it.

## Style

The formatter owns layout — no semicolons, single quotes, width 95. Never hand-format.

- Keep things in one function unless composable or reusable
- Avoid `try`/`catch` where possible; avoid `any`; avoid `else` — early return instead
- Prefer single-word names for locals; multi-word only when one word is ambiguous
- **Group a module's exports in one namespace named for the file**: `Msg.parse`, not
  `parseMsg`; `Env.validate`, not `validateEnv`. The namespace carries the noun, so
  the member is a verb and stays one word. Private helpers stay below it at module scope.
- Namespace members are `export const`, never `export function` — oxlint's
  `no-inner-declarations` fires on the declaration form. Classes may merge with a
  namespace of the same name (`Thing.load` beside `new Thing()`)
- A class earns its keep when it holds injected state (`Store`, `Scheduler`, `Tunnel`).
  A module of free functions is a namespace instead
- Use Bun APIs when possible (`Bun.file()`, `Bun.YAML`, `bun:sqlite`)
- Rely on type inference; annotate exports and interfaces
- Comments state the non-obvious constraint — why, not what. Below three lines.
- `.oxlintrc.json` disables a rule only with a written reason. A new false positive gets
  the entry _and_ the reason; anything else gets fixed.

## Testing

- `test/` next to the package, `bun:test`, no mocks of our own modules: the CP suite drives
  the real `Store` on `:memory:` and the real scheduler against a fake `HostPlacement`
- Secrets must never appear in a SQLite row: tests assert on the raw table, keep that
