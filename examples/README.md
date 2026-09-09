# examples

What a parent app looks like when it uses sandboxd. One example today:

| Example    | What                                                              |
| ---------- | ----------------------------------------------------------------- |
| [`ui`](ui) | The reference client: presets, a sandbox list, a browser terminal |

## Run it

From the repository root, with Docker running:

```bash
(cd examples/ui && bun install)
go run ./scripts/dev              # api :8080, ui :8081, a worker on this machine
```

Then open <http://localhost:8081>. To run the UI alone against a control plane that is
already up, use `bun run dev` from `examples/ui` and point it with `SANDBOXD_API_URL`.

| Variable                 | Default                 | What                                          |
| ------------------------ | ----------------------- | --------------------------------------------- |
| `SANDBOXD_API_URL`       | `http://localhost:8080` | The control plane                             |
| `SANDBOXD_UI_PORT`       | `8081`                  | Where this app listens                        |
| `SANDBOXD_SERVICE_TOKEN` | `dev-token`             | Must match the control plane's                |
| `SANDBOXD_PRESETS_DIR`   | `presets/`              | Where `<name>/preset.yaml` is read from       |
| `SANDBOXD_DEFAULT_IMAGE` | none                    | Used when neither preset nor caller names one |

## What the UI is for

It plays the part your own app would play. It holds the service token so the browser never
does, resolves a preset into an image and env, and forwards the owner id as
`X-Sandboxd-Owner`. Four pages: the fleet at `/hosts`, the list at `/sandboxes`, the form
at `/sandboxes/new`, and the terminal at `/sandboxes/:id`.

Its own JSON lives under `/api`, and each route is one generated SDK call. Two are its
own: `GET /api/presets` lists every preset with its fields, image and preview port, and
`POST /api/sandboxes` resolves a preset before calling the control plane.

```bash
# through the UI: presets, no token in the browser
curl -s localhost:8081/api/sandboxes \
  -H 'x-sandboxd-owner: me' -H 'content-type: application/json' \
  -d '{"repo":"https://github.com/org/repo.git","prompt":"add tests for src/x.ts"}'

curl -s localhost:8081/api/presets
```

That request names no preset, so `agent` claims it by having `repo`. Naming one is
`{"preset":"vscode", ...}`. Anything the preset does not define — `image`, `cmd`, `env`,
`tags` — passes through to the API untouched.

## Presets

A preset is data: one `preset.yaml` per directory under `presets/`, and nothing else. It
names an image that already exists and maps request fields onto that image's environment.
It builds nothing, and it never teaches this app what a repo or an agent is.

```yaml
# presets/vscode/preset.yaml
description: VS Code (code-server) on a repo, behind the preview proxy
image: ghcr.io/developing-software/sandboxd-vscode:latest
idle_timeout_s: 14400
preview: { port_field: port }
fields:
  repo: { type: string, env: REPO, description: "optional, cloned into /workspace" }
  port: { type: int, min: 1, max: 65535, default: 8080, env: VSCODE_PORT }
  secrets.git_token: { type: string, secret: true, env: GIT_TOKEN }
```

| Key              | What                                                                         |
| ---------------- | ---------------------------------------------------------------------------- |
| `description`    | Required. One line, shown in the form                                        |
| `image`          | Required. A registry reference, or `null` to mean the caller's own image     |
| `cmd`            | What runs in the PTY. Omit when the image ships its own entry                |
| `idle_timeout_s` | At least 60. Raise it when a person watches a browser page, not the terminal |
| `preview`        | `{port: 8080}` for a fixed port, or `{port_field: port}` to read one field   |
| `claims_when`    | `{present: repo}` — picks this preset when a request names none              |
| `fields`         | The request fields, each mapped onto one env var                             |

A field is `{type, env, required, secret, default, values, min, max, multiline,
description}`. `type` is `string`, `int`, `enum`, `url` or `bool`; `values` lists the cases
of an enum; `secret: true` sends it as `secret_env`, so it is never persisted or returned.
Dots nest a field in the request body, so `secrets.git_token` is sent as
`{"secrets": {"git_token": "..."}}`. `TERM` and `SANDBOXD_SESSION_ID` are set by the daemon
and cannot be mapped.

A malformed file stops the app at boot with a message naming it. Validation of what the
API owns — the image, the command, the tags — stays in the API.

## Writing your own client

Nothing here is required to use sandboxd. Presets and the shape of a friendly request are
this app's choices, not the product's; the contract is
[`api/client.yaml`](../api/client.yaml) and the generated
[SDK](../sdk/typescript/README.md). If you find a route no generated client can express,
the gap is in the document, not in a handler.

For how this app is built and the rules it follows, see [`ui/AGENTS.md`](ui/AGENTS.md).
