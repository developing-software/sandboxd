# ui

The reference client, and the only TypeScript left in the repo. It plays the parent app:
holds the service token, resolves presets, serves the xterm.js page, and turns a friendly
request into the API's generic one.

It is a **client of a documented API**, not part of the product. It imports nothing from
the repo, has its own `package.json`, `tsconfig.json` and linter config, and can rot a
release behind the daemons without blocking anything.

- `src/app.ts` — Hono: `/`, `/presets`, `POST /sandboxes`, and a forwarder for everything
  else that adds the service token. The browser never holds the token.
- `src/sandboxes.ts` — `buildCreate`: preset + fields → the API's `POST /sandboxes` body.
- `src/{errors,json,log}.ts` — the three helpers this app needs, kept here rather than
  shared: there is nothing left to share them with.
- `src/presets/` — `preset.yaml` → `Preset`; the registry.
- `presets/` — the data, and `build.ts` (`bun run image`).

## Rules

- **The API is called with `fetch`.** The request and response shapes are declared here,
  in `src/sandboxes.ts`. `bun x openapi-typescript http://localhost:8080/openapi.json`
  generates them properly the day this file grows past a few types.
- **Validation of what the API owns stays in the API.** This app checks what only it can
  know: preset fields. Image, command, env and tags go through untouched.
- **Presets are data.** Nothing preset-specific belongs in TypeScript. A malformed
  `preset.yaml` is a boot error naming the file.
- `index.html` is the dev page; it talks to this app on the same origin, no auth header.

## Style

The formatter owns layout — no semicolons, single quotes, width 95. Never hand-format.

- Keep things in one function unless composable or reusable
- Avoid `try`/`catch` where possible; avoid `any`; avoid `else` — early return instead
- Prefer single-word names for locals; multi-word only when one word is ambiguous
- **Group a module's exports in one namespace named for the file**: `Msg.parse`, not
  `parseMsg`; `Env.validate`, not `validateEnv`. The namespace carries the noun, so the
  member is a verb and stays one word. Private helpers stay below it at module scope.
- Namespace members are `export const`, never `export function` — oxlint's
  `no-inner-declarations` fires on the declaration form. Classes may merge with a
  namespace of the same name (`Thing.load` beside `new Thing()`)
- A class earns its keep when it holds injected state. A module of free functions is a
  namespace instead
- Use Bun APIs when possible (`Bun.file()`, `Bun.YAML`)
- Rely on type inference; annotate exports and interfaces
- Comments state the non-obvious constraint — why, not what. Below three lines.
- `.oxlintrc.json` disables a rule only with a written reason

## Commands

From this directory: `bun install` once, then

| Command             | What                                          |
| ------------------- | --------------------------------------------- |
| `bun run dev`       | The UI on :8081, talking to the local API     |
| `bun run image`     | One Docker image per preset with a Dockerfile |
| `bun test`          |                                               |
| `bun run typecheck` |                                               |
| `bun run fmt`       | `oxfmt`, not Prettier                         |
| `bun run lint`      | `oxlint`, not ESLint                          |
| `bun run fallow`    | Dead code and duplication                     |

Before handing work back: `bun run fmt && bun run lint && bun run typecheck && bun test`.
`bun run fallow` reports inherited warnings; do not add to them.

`go run ./scripts/dev` from the repo root starts this app alongside the two daemons.
