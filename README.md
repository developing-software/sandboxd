# sandboxd

Run disposable sandboxes on machines you own, watch them in a browser terminal, and open
any port they listen on through a link.

A **sandbox** is one container from any image, running one command in a PTY. You create it
with one HTTP call. It ends when the command exits, when it goes idle, or when you delete
it. Nothing survives it except what it pushed to git.

The main use is running a coding agent on a repository: hand over a repo, a prompt and a
token, and a person can watch, take the keyboard, or walk away. The same machinery serves
a notebook, an editor in the browser, or a preview environment, because the API only ever
sees an image, a command, env and tags.

## The pieces

| Piece             | What it does                                                                |
| ----------------- | --------------------------------------------------------------------------- |
| `sandboxd-api`    | Places sandboxes, holds the state, serves the terminals and the previews    |
| `sandboxd-worker` | One per machine that runs sandboxes. Dials out, so it needs no inbound port |
| `examples/ui`     | The reference client, standing in for your app. Holds the service token     |

Both daemons are static Go binaries. Your app talks to the control plane over HTTP with a
service token; your users never see it.

## Deploy

`nix build .#api` and `nix build .#worker` produce one static binary each, and
`--system aarch64-linux` cross-compiles without a builder of that architecture. Each daemon
takes one YAML file and nothing else.

**The control plane** wants a machine reachable from the outside, TLS in front of it, and
two DNS names: one for the API, and a wildcard for previews, because each preview is served
from `<port>-<sid>.<preview domain>`. State is one SQLite file.

```yaml
# /etc/sandboxd/api.yaml
listen: ":8080"
public_url: https://sandboxd.example.com
preview_domain: preview.sandboxd.example.com
db: /var/lib/sandboxd-api/cp.db
auth:
  service_token_file: /run/secrets/sandboxd-service-token
providers:
  workers:
    join_token_file: /run/secrets/sandboxd-join-token
```

**A worker** goes on every machine that should run sandboxes. It needs Docker and a route
to the control plane, nothing more: it dials out, so no inbound port, no public address,
and NAT is fine.

```yaml
# /etc/sandboxd/worker.yaml
url: wss://sandboxd.example.com
join_token: ${SANDBOXD_JOIN_TOKEN:?set it in the unit}
tags: [gpu:a100]
identity: /var/lib/sandboxd-worker/host.json
driver:
  docker: {sock: /var/run/docker.sock, max_sandboxes: 4}
```

A worker carrying the join token is approved the moment it says hello. One without it
prints an approval code and waits: an operator sends that code to
`POST /hosts/{id}/approve`, or types it into the fleet page of a client. Either way the
host keeps its identity file, so it stays approved across reboots and upgrades.

```bash
curl -s https://sandboxd.example.com/healthz
curl -s https://sandboxd.example.com/hosts -H "authorization: Bearer $SANDBOXD_SERVICE_TOKEN"
```

When a host is online and carries the tags you ask for, the first sandbox is the create
call in [The API](#the-api) below.

**On NixOS** this flake ships the modules: `services.sandboxd.api`, `.worker`, and
`.ingress`, which puts Traefik in front of the control plane with a wildcard certificate
for the preview domain. Their `settings` render the daemon's YAML into the store, so it
holds `${VAR}` references and never a secret, and `environmentFile` supplies those. Whole
hosts and the OpenTofu environments that provision them live under `infra/`. A deployed
control plane runs the `workers` provider only: sandboxes live on the hosts that dial in.

To try all of it on one machine first, `go run ./scripts/dev` starts the control plane, a
worker and the reference UI in one terminal. See [`examples/`](examples/README.md).

## How a sandbox works

**One request, one environment.** You send an image, an optional command, env and tags.
The control plane picks a host that carries every tag you asked for, starts the container
there, and execs your command into a PTY.

**Status** is one of `queued`, `creating`, `running`, `ended`. A queued sandbox is waiting
for a free host and reports its place in line; a request no host in the fleet could ever
satisfy is refused instead of queued. An ended sandbox says why: `closed`, `exited`,
`idle`, `failed`, or `lost` when the host went away.

**Idle** means no PTY traffic in either direction, for 30 minutes by default. Preview
traffic does not count, which is why the browser-facing presets raise it to four hours.

**Secrets pass through.** Anything in `secret_env` reaches the process that needs it and is
never persisted or returned. Anything in `env` is stored and visible in the API.

**Two credentials.** The bearer service token says which app is calling. The
`X-Sandboxd-Owner` header says which of its users the call is for, and every sandbox route
requires it. Terminal and preview links carry their own short-lived credential in the URL,
so they are the only thing you hand to a browser.

## The API

| Route                           | What                                                   |
| ------------------------------- | ------------------------------------------------------ |
| `POST /sandboxes`               | Create one. Only `image` is required                   |
| `GET /sandboxes`                | This owner's sandboxes                                 |
| `GET /sandboxes/{id}`           | One sandbox, including status, host and queue position |
| `DELETE /sandboxes/{id}`        | End it now                                             |
| `POST /sandboxes/{id}/terminal` | A `wss://` URL for the terminal, good for 60 s         |
| `POST /sandboxes/{id}/preview`  | A URL for one port inside the sandbox, good for 10 min |
| `GET /healthz`                  | Liveness                                               |

A create body takes `cmd`, `env`, `secret_env`, `tags` and `idle_timeout_s` besides the
image. Everything else is the image's business.

```bash
# create
curl -s localhost:8080/sandboxes \
  -H 'authorization: Bearer dev-token' \
  -H 'x-sandboxd-owner: me' -H 'content-type: application/json' \
  -d '{"image":"python:3.12","cmd":["python3","-m","http.server","8000"]}'

# watch it: the url it returns carries its own token, hand it to xterm.js
curl -s -XPOST localhost:8080/sandboxes/s_ab12cd34/terminal \
  -H 'authorization: Bearer dev-token' -H 'x-sandboxd-owner: me'

# reach port 8000: open the url in a browser
curl -s -XPOST localhost:8080/sandboxes/s_ab12cd34/preview \
  -H 'authorization: Bearer dev-token' -H 'x-sandboxd-owner: me' \
  -H 'content-type: application/json' -d '{"port":8000}'
```

Errors answer `{"error": "...", "issues": [...]}`, the second only for validation. The
reference is at <http://localhost:8080/doc>, and the two documents behind it are served at
`/openapi.yaml` and `/openapi.admin.yaml`. Both are checked in, as
[`api/client.yaml`](api/client.yaml) and [`api/admin.yaml`](api/admin.yaml), and the server
serves those bytes rather than a rendering of them.

Enrolling and listing hosts is a separate operator document, `api/admin.yaml`. It has no
published client so it stays free to change.

## From TypeScript

[`@sandboxd/sdk`](sdk/typescript) is generated from `api/client.yaml`, the same file the Go
server is generated from, so a contract change is a type error rather than a runtime 400.

```ts
import { Sandboxd } from '@sandboxd/sdk'

const sandboxd = new Sandboxd({
  baseUrl: 'http://localhost:8080',
  serviceToken: process.env.SANDBOXD_SERVICE_TOKEN!,
})

const { data, error } = await sandboxd.createSandbox({
  headers: { 'X-Sandboxd-Owner': 'me' },
  body: { image: 'python:3.12', cmd: ['bash'] },
})
```

The service token is an operator credential, so keep these calls on a server. The
[SDK README](sdk/typescript/README.md) covers the terminal socket and previews.

## Images and presets

The API speaks images; everything friendlier lives in a client. A **preset** is one YAML
file that names an image and maps request fields onto its env, so `repo` and `prompt`
become env vars an image's entry script reads.

| Preset                     | What you get                                                                  |
| -------------------------- | ----------------------------------------------------------------------------- |
| `agent`                    | A fresh clone on its own branch, then Claude Code, Codex, OpenCode or a shell |
| `vscode`                   | VS Code (code-server) on the repo, in the browser                             |
| `jupyter`, `notebook`      | JupyterLab or the classic Notebook, in the browser                            |
| `ubuntu`, `python`, `node` | A shell or a REPL on a stock image                                            |
| `http`                     | python's http.server, behind the preview proxy                                |
| `custom`                   | Nothing implied: your image, your command, your env                           |

Presets live with the reference client, in [`examples/`](examples/README.md). The official
images are in [`images/`](images/README.md) and are published to GHCR, so nothing has to be
built to run any of the above. Your own image is a first class one: it needs only to stay
up on its own and read its configuration from env.

Inside the `agent` image, an agent is a file too: one `.sh` per agent in
[`images/agent/agents/`](images/agent/agents/README.md), discovered at start-up, so teaching
it a new one is neither an API change nor a client change.

## Configuration

Each daemon reads one YAML file: `--config`, then `SANDBOXD_CONFIG`, then
`/etc/sandboxd/api.yaml` or `worker.yaml`. Unknown keys refuse to boot with a line number.
`${VAR}`, `${VAR:-default}` and `${VAR:?message}` pull values from the environment, `$$` is
a literal `$`, and every secret has a `_file` twin. `--check-config` prints what the daemon
would run with, secrets redacted.

Besides what the [Deploy](#deploy) files above show, two keys are worth knowing. Anything
under `sandbox_env` and `sandbox_env_files` is placed in every sandbox and never
persisted, which is where a shared LLM gateway and its key belong. And a
`providers.docker` block runs sandboxes on the control plane's own Engine, no worker
involved.

```yaml
sandbox_env:
  LLM_BASE_URL: https://gateway.example.com
sandbox_env_files:
  LLM_API_KEY: /run/secrets/llm-key
providers:            # absent = workers only; present = exactly what it lists
  workers:
    join_token_file: /run/secrets/sandboxd-join-token
  docker:
    name: cp
    tags: [gpu:none]
    sock: /var/run/docker.sock
    max_sandboxes: 4
```

A **driver** is what runs a sandbox, Docker today. A **provider** is where it is managed
from: `workers` behind the tunnel, or `docker` inside the control plane process. Hosts from
either carry `provider:workers` or `provider:local` in their tags, so a caller picks one
with `tags` like anything else. A sandbox on the in-process provider does not survive a
control plane restart; one on a worker does.

With no file, the legacy `SANDBOXD_*` variables still apply — `SANDBOXD_SERVICE_TOKEN` is
the only required one, and `SANDBOXD_DEV=1` accepts `dev-token` locally. Run
`--check-config` to see what any of them resolved to.

Schema changes are not migrated in v1: a database from another version is wiped at boot
with a warning in the log. Hosts re-enrol on their next hello; sandboxes are gone.

## Where to read next

| Page                            | Read it when                                                |
| ------------------------------- | ----------------------------------------------------------- |
| [Examples](examples/README.md)  | You want to run the reference UI or write a preset          |
| [Images](images/README.md)      | You want to build, pin or publish a sandbox image           |
| [SDK](sdk/typescript/README.md) | You are calling the API from TypeScript                     |
| [`VISION.md`](VISION.md)        | You want to know what this is for and what it will never be |
| [`AGENTS.md`](AGENTS.md)        | You are changing the code                                   |
