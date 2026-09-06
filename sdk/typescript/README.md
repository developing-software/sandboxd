# @sandboxd/sdk

The typed TypeScript client for the sandboxd control plane. Generated from
`openapi.json` at the repo root by [hey-api](https://heyapi.dev), which is generated in
turn from the Go handlers in `internal/cp/http` — so a field that changes there is a type
error here, not a 400 at runtime.

The control plane is service-token gated: everything below runs on a server, never in a
browser. A token in the browser could create sandboxes for any owner.

## Use it

```ts
import { createSandbox, createSandboxd, mintAttachToken } from '@sandboxd/sdk'

const client = createSandboxd({
  baseUrl: 'https://sandboxd.example.com',
  serviceToken: process.env.SANDBOXD_SERVICE_TOKEN!,
})

const { data: sandbox, error } = await createSandbox({
  client,
  body: {
    owner_id: 'user_42',
    image: 'python:3.12',
    cmd: ['python3', '-m', 'http.server', '8000'],
    env: { PYTHONUNBUFFERED: '1' },
  },
})
if (error) throw new Error(error.error) // { error, issues? }

// The browser terminal: mint a token, hand the URL to xterm.js.
const { data: attach } = await mintAttachToken({
  client,
  path: { id: sandbox!.id },
  body: { owner_id: 'user_42' },
})
new WebSocket(attach!.wss_url)
```

Nothing throws. Every call answers `{ data, error, response }`; `response` is undefined
only when the request never reached the control plane.

`GET /attach` is in the document so a reader can find it, but it is a WebSocket upgrade
and no `attach()` function is exported: mint a token and open `wss_url`.

## What is generated

| Path                   | What                                                               |
| ---------------------- | ------------------------------------------------------------------ |
| `src/generated/`       | hey-api's output. Never edited; the formatter and linter ignore it |
| `src/index.ts`         | Hand-written. The client factory, and the only thing a caller sees |
| `openapi-ts.config.ts` | The generator's config                                             |

```bash
go run ./scripts/openapi -o openapi.json   # from the repo root, after changing a handler
bun run generate                           # here
bun run typecheck && bun run lint && bun run fmt:check
```

CI regenerates and fails on a diff, so the checked-in output is always what the config
produces from the checked-in document.
