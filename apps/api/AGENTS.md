# api

The control plane. One `Bun.serve` in `src/main.ts` fans out to three things: the Hono
app in `src/http/` (the parent-facing JSON API), the worker tunnel (`hosts/`), and the two
browser paths — attach (`attach.ts`) and preview (`preview/`).

## Adding a route

1. Declare the request and response shapes in `src/schema.ts` with `.meta({ description })`.
2. Add one entry to the matching `src/http/handler/*.ts`: `doc(...)` for the document,
   `validator(target, Schema)` for the 400s, then the handler calling a service.
3. Nothing else. The document, the 400 shape and the UI's client type follow from the chain.

## Rules

- **The API knows nothing of presets.** No `preset`, `repo`, `prompt` here — a body names
  an image, a command, env and sidecars. `CreateSession` is `.strict()` so a preset field
  sent here is a 400 that says the caller meant the UI. Presets live in `apps/ui`.
- **Every shape lives in `schema.ts`, once.** A handler never restates a schema; a service
  takes the parsed type. Response schemas need `.meta({ description })` or the document is invalid.
- **Handlers stay chained.** `Routes` is the type of the chain in `http/routes.ts`; a
  handler registered outside it is invisible to `hono/client` in `apps/ui`.
- **Secrets never touch `store.ts`.** `secret_env` lives in the scheduler's memory until
  `session.create` is sent, then is dropped. A new field that carries a secret follows
  the same path. The tests assert on raw SQLite rows for this.
- **Ownership is checked in `sessions.ts`, not in a handler.** The CP has no user
  table; `owner_id` on every call is the whole model. A session of another owner is a 404.
- **Every caller-facing error is an `HttpError` from `@sandboxd/core/errors`.** Throw
  `badRequest`, `notFound`, `conflict`; `.onError` serializes as `{error}`.
- **The preview proxy and the tunnel run before Hono** — one matches on hostname, the
  other is a bare upgrade. Keep that order in `main.ts`.
- Schema changes bump `SCHEMA_VERSION` in `store.ts`. There are no migrations in v1.

## Testing

`test/http.test.ts` drives the real app with `app.request()`, no listening socket; the
attach upgrade takes a fake server as the third argument. `bun test`, `bun run typecheck`.
