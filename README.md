# sandboxd

Run sandboxes on your own machines, supervised from a browser terminal, behind one
control plane.

A **sandbox** is one container from any image, running one command in a PTY. You create
it over an HTTP API, watch it in a terminal in the browser, and open any port it listens
on through a preview proxy. It ends when the command exits, when you delete it, or when
it goes idle. Nothing survives it except what it pushed to git.

The pieces are two static Go binaries and a reference client:

- **`sandboxd-api`** — the control plane. Schedules sandboxes onto your workers, holds
  the state, serves the terminal socket and the preview proxy.
- **`sandboxd-worker`** — one per machine you want to run sandboxes on. Dials out to the
  control plane, so a worker needs no inbound port and no public address.
- **`examples/ui`** — the stand-in for whatever app of yours calls the API. It holds the
  service token, turns presets such as `agent` into an image and a command, and
  serves the xterm.js page.

The API itself knows nothing about repos, agents or presets: it speaks images, commands,
env and host tags. Everything friendlier lives in the client.

## Get it running

Docker must be running on the machine that acts as a worker. Neither toolchain is on
`PATH` outside the Nix devShell: `direnv allow` once, or prefix each command with
`nix develop --command`.

```bash
(cd examples/ui && bun install)   # the sandbox images are pulled, not built
go run ./scripts/dev              # api :8080, ui :8081, a worker here
```

Open <http://localhost:8081/hosts>. The worker shows up as a card marked **pending** with
an approval code printed in the terminal — paste it into the card. Then go to **new**, pick
the `agent` preset, give it a repo and a prompt, and watch it work.

## What you can run

| Preset         | What you get                                                                  |
| -------------- | ----------------------------------------------------------------------------- |
| `agent` | A fresh clone on its own branch, then Claude Code, Codex, OpenCode or a shell |
| `vscode`       | VS Code (code-server) on the repo, in the browser                            |
| `jupyter`      | JupyterLab or the classic Notebook, in the browser                           |
| `ubuntu`, `python`, `node` | A shell or a REPL on the stock image; nothing to build              |
| `http`         | python's http.server on the stock image, behind the preview proxy            |
| `notebook`     | JupyterLab on the stock docker-stacks image; nothing to build                |
| `custom`       | Nothing implied: your image, your command, your env                          |

A preset is one `preset.yaml` under `examples/ui/presets/`: it names an image and maps
request fields onto that image's env. The images live in [`images/`](images/README.md) and
are published to GHCR, so nothing has to be built to run any of the above.

Inside the `agent` image, a *harness* is a file too: one `.sh` per agent in
[`images/agent/agents/`](images/agent/agents/README.md), discovered at start-up, so
teaching it a new one is neither an API change nor a client change.

## Two ways in

The UI speaks presets. The API speaks images. Your app can use either.

```bash
# --- through the UI (:8081): presets, no token in the browser ---

curl -s localhost:8081/api/sandboxes \
  -H 'x-sandboxd-owner: me' -H 'content-type: application/json' \
  -d '{"repo":"https://github.com/org/repo.git","prompt":"add tests for src/x.ts"}'

curl -s localhost:8081/api/presets    # every preset's fields, image and preview port

# --- straight to the API (:8080): image + command + env, with the service token ---

curl -s localhost:8080/sandboxes -H 'authorization: Bearer dev-token' \
  -H 'x-sandboxd-owner: me' -H 'content-type: application/json' \
  -d '{"image":"python:3.12","cmd":["python3","-m","http.server","8000"]}'
```

The token says which app is calling; `x-sandboxd-owner` says which of its users for, and it
is required on every route that belongs to one.

The API reference is at <http://localhost:8080/doc>, and the two documents behind it at
`/openapi.yaml` and `/openapi.admin.yaml`. Both are checked in — the client contract as
[`api/client.yaml`](api/client.yaml), the operator's as
[`api/admin.yaml`](api/admin.yaml) — and the server serves those bytes rather than a
rendering of them.

## Calling it from TypeScript

[`@sandboxd/sdk`](sdk/typescript) is a typed client generated from `api/client.yaml`, the
same file the Go server is generated from. The shapes below are the contract itself rather
than a copy of it.

```ts
import { createSandbox, createSandboxd } from '@sandboxd/sdk'

const client = createSandboxd({
  baseUrl: 'http://localhost:8080',
  serviceToken: process.env.SANDBOXD_SERVICE_TOKEN!,
})

const { data, error } = await createSandbox({
  client,
  headers: { 'X-Sandboxd-Owner': 'me' },
  body: { image: 'python:3.12', cmd: ['bash'] },
})
```

The service token is an operator credential: keep these calls on a server. See the
[SDK README](sdk/typescript/README.md) for the terminal socket and previews.

## Configuration

Each daemon reads one YAML file: `--config`, then `SANDBOXD_CONFIG`, then
`/etc/sandboxd/api.yaml` or `worker.yaml`. Unknown keys refuse to boot with a line number,
`${VAR}` / `${VAR:-default}` / `${VAR:?message}` pull values from the environment (`$$` is
a literal), and every secret has a `_file` twin. `--check-config` prints what the daemon
would run with, secrets redacted.

```yaml
# /etc/sandboxd/api.yaml
listen: ":8080"
public_url: https://api.example.com
preview_domain: preview.example.com
db: /var/lib/sandboxd-api/cp.db
auth:
  service_token_file: /run/secrets/sandboxd-service-token   # or service_token:
sandbox_env:                       # under every sandbox, never persisted
  LLM_BASE_URL: https://gateway.example.com
sandbox_env_files:
  LLM_API_KEY: /run/secrets/llm-key
providers:                         # absent = workers only; present = exactly this
  workers:                         # machines running sandboxd-worker
    join_token_file: /run/secrets/sandboxd-join-token
  docker:                          # sandboxes on this machine's Engine, no worker needed
    name: cp
    tags: [gpu:none]
    sock: /var/run/docker.sock
    max_sandboxes: 4
```

```yaml
# /etc/sandboxd/worker.yaml
url: wss://api.example.com
tags: [gpu:a100]
join_token: ${SANDBOXD_JOIN_TOKEN:?set it in the unit}
driver:
  docker: {sock: /var/run/docker.sock, max_sandboxes: 4}
```

A **driver** is what runs a sandbox (Docker today); a **provider** is where it is managed
from: `workers` behind the tunnel, or `docker` inside the control plane process. Hosts
from either carry `provider:workers` or `provider:local` in their tags, so a sandbox picks
one with `tags` like anything else. A sandbox on the in-process provider does not survive
a control plane restart; one on a worker does.

With no file, the legacy variables still apply. **API** — `SANDBOXD_SERVICE_TOKEN`
(**required**; `SANDBOXD_DEV=1` accepts `dev-token` locally), `SANDBOXD_PORT`,
`SANDBOXD_PUBLIC_URL`, `SANDBOXD_PREVIEW_DOMAIN`, `SANDBOXD_DB`, `SANDBOXD_SECRET`,
`SANDBOXD_JOIN_TOKEN`, `SANDBOXD_SANDBOX_ENV_<NAME>`, `SANDBOXD_LLM_BASE_URL`,
`SANDBOXD_LLM_API_KEY`. **Worker** — `SANDBOXD_URL`, `SANDBOXD_WORKER_NAME`,
`SANDBOXD_WORKER_MAX_SESSIONS`, `SANDBOXD_WORKER_IDENTITY`, `SANDBOXD_WORKER_ENTRY`,
`SANDBOXD_WORKER_TAGS`, `DOCKER_SOCK`.

**UI** — `SANDBOXD_API_URL` (http://localhost:8080), `SANDBOXD_UI_PORT` (8081),
`SANDBOXD_SERVICE_TOKEN` (must match the API's), `SANDBOXD_PRESETS_DIR`,
`SANDBOXD_DEFAULT_IMAGE`.

Schema changes are not migrated in v1: a database from another version is wiped at boot,
with a warning in the log. Hosts re-enrol on their next hello; sandboxes are gone.

## Deploying

`nix build .#api` and `.#worker` produce the release binaries;
`--system aarch64-linux` cross-compiles without a builder of that architecture. The NixOS
hosts and OpenTofu environments live under `infra/`.

## Where to read next

| Page                                       | Read it when                                                   |
| ------------------------------------------ | -------------------------------------------------------------- |
| [`VISION.md`](VISION.md)                   | You want to know what this is for and what it will never be    |
| [`PLAN.md`](PLAN.md)                       | You want to know what is being built next                      |
| [Images](images/README.md)                 | You want to build, pin or publish a sandbox image              |
| [SDK](sdk/typescript/README.md)            | You are calling the API from TypeScript                        |
| [`AGENTS.md`](AGENTS.md)                   | You are changing the code                                      |
