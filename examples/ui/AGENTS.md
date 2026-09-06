# ui

The reference client. It plays the parent app: holds the service token, resolves presets,
serves the pages, and turns a friendly request into the API's generic one.

It is a **client of a documented API**, not part of the product. It has its own
`package.json`, `tsconfig.json` and linter config, and can rot a release behind the
daemons without blocking anything. The one thing it imports from the repo is
`@sandboxd/sdk` (`sdk/typescript`), the generated client — which is the point: this app is
the proof that the published SDK is usable, on the server and in the browser.

Two halves, composed in `src/main.ts` by one `Bun.serve`:

- **Pages**, `src/pages/`. One `.html` + `.ts` pair per concern, bundled by Bun from the
  html's own `<script>` and `<link>` tags: `hosts` (`/hosts`), `sandboxes`
  (`/sandboxes`), `new` (`/sandboxes/new`), `sandbox` (`/sandboxes/:id`, the terminal).
  `/` redirects to the list. What they share: `shell.ts` (nav, owner id, polling),
  `client.ts` (JSON over `/api`), `format.ts` (how a sandbox reads), `term.ts` (xterm.js
  on the attach socket), `style.css`.
- **The API**, `src/api.ts` — Hono under `/api`: `GET /api/presets`,
  `POST /api/sandboxes`, and a forwarder for everything else that adds the service token.
  The browser never holds the token.

And the rest:

- `src/sandboxes.ts` — `buildCreate`: preset + fields → the API's `POST /sandboxes` body.
- `src/{errors,json,log}.ts` — the three helpers this app needs, kept here rather than
  shared: there is nothing left to share them with.
- `src/presets/` — `preset.yaml` → `Preset`; the registry.
- `presets/` — the data, and `build.ts` (`bun run image`).

## Rules

- **Typed calls go through `@sandboxd/sdk`; the rest is a byte proxy.** `POST /sandboxes`
  is the one request this app builds, so it uses the SDK's `createSandbox` and its
  `CreateSandbox` type. Everything else is forwarded with `fetch`, status and body
  untouched — a proxy has no opinion about shapes, and typing them would only rot.
  A type this app needs comes from the SDK; never re-declare one here. A page reads a
  response as the SDK's view type (`HostView`, `SandboxView`, …) — a type-only import,
  so no SDK code reaches the browser.
- **A page URL is never a JSON URL.** Pages are `Bun.serve` routes; JSON is `/api/*`.
  A new kind of data is a new `/api` route, not a page that answers both.
- **Validation of what the API owns stays in the API.** This app checks what only it can
  know: preset fields. Image, command, env and tags go through untouched.
- **Presets are data.** Nothing preset-specific belongs in TypeScript. A malformed
  `preset.yaml` is a boot error naming the file. A preset on a stock image (`ubuntu`,
  `python`, `node`, `http`, `notebook`) is a `preset.yaml` alone: `image:` names it, `cmd:` runs in
  the PTY, and `bun run image` skips the folder.
- The pages talk to this app on the same origin, no auth header. The owner id is whatever
  is typed in the nav, kept in localStorage.

## Style

The formatter owns layout — no semicolons, single quotes, width 95. Never hand-format.
`.html` is the exception: it is not formatted, keep it small.

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
- Use Bun APIs when possible (`Bun.file()`, `Bun.YAML`, `Bun.serve` routes)
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
