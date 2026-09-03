# ui

The parent-app stand-in. Holds the service token, the presets and the catalog, serves the
xterm.js page, and turns a friendly request into the API's generic one.

- `src/app.ts` — Hono: `/`, `/presets`, `/services`, `POST /sessions`, and a forwarder for
  everything else that adds the service token. The browser never holds the token.
- `src/sessions.ts` — `buildCreate`: preset + fields → `CreateSessionInput` for the API.
- `src/services.ts` — catalog names and references → compose services. The UI never
  translates compose; it copies entries and lets the API validate.
- `src/presets/` — `preset.yaml` → `Preset`; the registry; the catalog.
- `presets/` — the data, and `build.ts` (`bun run image`).

## Rules

- **The API is called through `hono/client`**, typed off `@sandboxd/api/http/routes`'s
  `Routes`. A route the API does not have does not compile here. Everything else about
  the API (`schema.ts`, `services.ts`) is out of bounds — types only.
- **Validation of what the API owns stays in the API.** This app checks what only it can
  know (preset fields, catalog names, that a merged compose has no duplicate service);
  a full service declaration passes through untouched.
- **Presets are data.** Nothing preset-specific belongs in TypeScript. A malformed
  `preset.yaml` is a boot error naming the file.
- **Catalog picks travel as compose.** `services: ["postgres"]` becomes
  `compose.services.postgres` copied from `services.yaml`; `x-sandboxd.sandbox_env` and
  `secret_env` ride along and the API applies them. Do not add a second path.
- `index.html` is the dev page; it talks to this app on the same origin, no auth header.

## Commands

From this directory: `bun test`, `bun run typecheck`. `bun run image [preset…]` from the root.
