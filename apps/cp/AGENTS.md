# cp

The control plane. One `Bun.serve` in `src/main.ts` fans out to three things: the
parent-facing HTTP API (`http.ts`), the host tunnel (`hosts/`), and the two browser paths
— attach (`attach.ts`) and preview (`preview/`).

## Rules

- **Secrets never touch `store.ts`.** `secret_env` lives in the scheduler's memory until
  `session.create` is sent, then is dropped. A new field that carries a secret follows
  the same path. The tests assert on raw SQLite rows for this.
- **Ownership is checked in `sessions.ts`, not in a handler.** The CP has no user
  table; `owner_id` on every call is the whole model. A session of another owner is a 404.
- **Every caller-facing error is an `HttpError` from `errors.ts`.** Throw `badRequest`,
  `notFound`, `conflict`; let `http.ts` serialize.
- **Presets are data.** `presets/` reads `preset.yaml`; nothing preset-specific belongs
  in TypeScript. A malformed file is a boot error, not a runtime surprise.
- **The preview proxy runs before routing** — it matches on hostname. Keep that order in
  `main.ts`.
- `config.ts` reads `SANDBOXD_*` only. The default presets dir is the repo's `presets/`,
  resolved relative to this file: moving it means updating that path.
- Schema changes bump `SCHEMA_VERSION` in `store.ts`. There are no migrations in v1.

## Commands

From this directory: `bun test`, `bun test --watch test/sessions.test.ts`, `bun run typecheck`.
