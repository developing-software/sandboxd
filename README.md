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
  service token, turns presets such as `coding-agent` into an image and a command, and
  serves the xterm.js page.

The API itself knows nothing about repos, agents or presets: it speaks images, commands,
env and host tags. Everything friendlier lives in the client.

## Get it running

Docker must be running on the machine that acts as a worker. Neither toolchain is on
`PATH` outside the Nix devShell: `direnv allow` once, or prefix each command with
`nix develop --command`.

```bash
(cd examples/ui && bun install && bun run image)   # one image per preset
go run ./scripts/dev                               # api :8080, ui :8081, a worker here
```

Open <http://localhost:8081>. The worker shows up as a card marked **pending** with an
approval code printed in the terminal — paste it into the card. Then pick the
`coding-agent` preset, give it a repo and a prompt, and watch it work.

[Getting started](docs/getting-started.md) has the rest: running the three processes
separately, skipping the approval code with a join token, and pointing a local UI at a
deployed control plane.

## What you can run

| Preset         | What you get                                                                  |
| -------------- | ----------------------------------------------------------------------------- |
| `coding-agent` | A fresh clone on its own branch, then Claude Code, Codex, OpenCode or a shell |
| `vscode`       | VS Code (code-server) on the repo, in the browser                            |
| `jupyter`      | JupyterLab or the classic Notebook, in the browser                           |
| `custom`       | Nothing implied: your image, your command, your env                          |

A preset is a folder under `examples/ui/presets/` with a `preset.yaml`, a `Dockerfile`
and an entry script, so adding one is adding a folder. [Sandboxes](docs/sandboxes.md)
covers presets, images and commands, host tags, previews and how a sandbox ends.

## Two ways in

The UI speaks presets. The API speaks images. Your app can use either.

```bash
# --- through the UI (:8081): presets, no token in the browser ---

curl -s localhost:8081/sandboxes -H 'content-type: application/json' \
  -d '{"owner_id":"me","repo":"https://github.com/org/repo.git","prompt":"add tests for src/x.ts"}'

curl -s localhost:8081/presets    # every preset's fields, image and preview port

# --- straight to the API (:8080): image + command + env, with the service token ---

curl -s localhost:8080/sandboxes -H 'authorization: Bearer dev-token' \
  -H 'content-type: application/json' \
  -d '{"owner_id":"me","image":"python:3.12","cmd":["python3","-m","http.server","8000"]}'
```

The API reference is at <http://localhost:8080/doc>, and the document behind it at
`/openapi.json`. The same document is checked in as [`openapi.json`](openapi.json).

## Calling it from TypeScript

[`@sandboxd/sdk`](sdk/typescript) is a typed client generated from that document, so the
shapes below come from the control plane's own handlers rather than from a copy of them.

```ts
import { createSandbox, createSandboxd } from '@sandboxd/sdk'

const client = createSandboxd({
  baseUrl: 'http://localhost:8080',
  serviceToken: process.env.SANDBOXD_SERVICE_TOKEN!,
})

const { data, error } = await createSandbox({
  client,
  body: { owner_id: 'me', image: 'python:3.12', cmd: ['bash'] },
})
```

The service token is an operator credential: keep these calls on a server. See the
[SDK README](sdk/typescript/README.md) for the terminal socket and previews.

## Configuration

**API** — `SANDBOXD_SERVICE_TOKEN` (**required**; `SANDBOXD_DEV=1` accepts `dev-token`
locally), `SANDBOXD_PORT` (8080), `SANDBOXD_PUBLIC_URL`, `SANDBOXD_PREVIEW_DOMAIN`,
`SANDBOXD_DB`, `SANDBOXD_SECRET`, `SANDBOXD_JOIN_TOKEN`. Env injected into every sandbox:
`SANDBOXD_SANDBOX_ENV_<NAME>=value`, plus the shorthands `SANDBOXD_LLM_BASE_URL` and
`SANDBOXD_LLM_API_KEY` (or plain `LLM_BASE_URL` / `LLM_API_KEY`).

**Worker** — `SANDBOXD_URL` (the API, `ws://…`), `SANDBOXD_WORKER_NAME`,
`SANDBOXD_WORKER_MAX_SESSIONS`, `SANDBOXD_WORKER_CONFIG`, `SANDBOXD_WORKER_ENTRY`,
`SANDBOXD_WORKER_DRIVER`, `SANDBOXD_WORKER_TAGS` (`virt:vm,gpu,region:eu`; `arch:`, `os:`
and `driver:` are reported without being configured).

**UI** — `SANDBOXD_API_URL` (http://localhost:8080), `SANDBOXD_UI_PORT` (8081),
`SANDBOXD_SERVICE_TOKEN` (must match the API's), `SANDBOXD_PRESETS_DIR`,
`SANDBOXD_DEFAULT_IMAGE`.

Schema changes are not migrated in v1: a database from another version is wiped at boot,
with a warning in the log. Hosts re-enrol on their next hello; sandboxes are gone.

## Deploying

`nix build .#api` and `.#worker` produce the release binaries;
`--system aarch64-linux` cross-compiles without a builder of that architecture.
[Deploying](docs/deploy.md) covers the NixOS hosts and OpenTofu environments under
`infra/`, worker enrollment, TLS, and getting preset images onto a remote worker.

## Where to read next

| Page                                       | Read it when                                                   |
| ------------------------------------------ | -------------------------------------------------------------- |
| [Getting started](docs/getting-started.md) | You want the stack on your laptop                              |
| [Sandboxes](docs/sandboxes.md)             | You want to know what a sandbox can run                        |
| [Deploying](docs/deploy.md)                | You want a control plane and workers on AWS or Proxmox         |
| [Repo config](docs/repo-config.md)         | You want a repo to declare its own sandbox (proposed)          |
| [SDK](sdk/typescript/README.md)            | You are calling the API from TypeScript                        |
| [`DESIGN.md`](DESIGN.md)                   | You want the contract and why each decision went the way it did |
| [`AGENTS.md`](AGENTS.md)                   | You are changing the code                                      |
