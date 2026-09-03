# core

The leaf. What the control plane and the worker agree on: `messages.ts` (every JSON
control message on the tunnel), `framing.ts` (the binary frame), plus `ids.ts`, `log.ts`
and `bytes.ts` because both sides need them.

## Rules

- **Imports nothing from `apps/`.** Ever. `cp` and `worker` depend on this, never the reverse.
- **One file, one entry point.** `exports` is `"./*": "./src/*.ts"`, so a consumer writes
  `import { parseMsg } from '@sandboxd/core/messages'`. There is no barrel; do not add one.
- **A message change is a protocol change.** Both apps are deployed separately; a new
  field must be optional or both sides ship together. Say which in the commit.
- No HTTP here. `HttpError` lives in `apps/cp/src/errors.ts` because only the CP serves HTTP.

## Commands

From this directory: `bun test`, `bun run typecheck`.
