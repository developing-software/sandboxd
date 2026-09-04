# core

The leaf. What the control plane and the worker agree on: `messages.ts` (every JSON
control message on the tunnel), `framing.ts` (the binary frame), plus `ids.ts`, `log.ts`,
`bytes.ts`, `json.ts` and `errors.ts` because both sides need them.

## Rules

- **Imports nothing from `apps/`.** Ever. `api` and `worker` depend on this, never the reverse.
- **One file, one namespace, one entry point.** `exports` is `"./*": "./src/*.ts"`, so a
  consumer writes `import { Msg } from '@sandboxd/core/messages'` and calls `Msg.parse`.
  There is no barrel; do not add one.
- **Namespace members are `export const`, never `export function`.** oxlint's
  `no-inner-declarations` fires on a function declaration inside a namespace; the arrow
  form is silent and says the same thing. Classes and interfaces are fine either way.
- **A message change is a protocol change.** Both apps are deployed separately; a new
  field must be optional or both sides ship together. Say which in the commit.
- No HTTP serving here. `Err.Http` carries a status because both apps raise it, but only
  the API turns one into a response.

## The namespaces

| File          | Namespace | What                                                         |
| ------------- | --------- | ------------------------------------------------------------ |
| `messages.ts` | `Msg`     | The wire contract: `Host`, `Cp`, `Spec`, `parse`             |
| `framing.ts`  | `Frame`   | `encode`, `decode`, `HEADER`                                 |
| `bytes.ts`    | `Bytes`   | `concat`, `crlf`, `crlf2`                                    |
| `ids.ts`      | `Id`      | `session`, `host`, `code`, `random`                          |
| `log.ts`      | `Log`     | `create`                                                     |
| `errors.ts`   | `Err`     | `Http`, `badRequest`, `notFound`, `conflict`, `unauthorized` |
| `json.ts`     | `Json`    | `isObj`                                                      |

## Commands

From this directory: `bun test`, `bun run typecheck`.
