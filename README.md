# sandboxd

Run sandboxes on your own machines, supervised from a browser terminal, behind one
control plane. A sandbox is a container from any image with a command in a PTY; presets
turn a friendly request into one: `coding-agent` clones a repo and runs Claude Code, Codex
or OpenCode in it; `vscode` opens the repo in VS Code (code-server) behind the preview
proxy; `jupyter` runs JupyterLab the same way; `custom` is any image + command + env.
See `DESIGN.md` for the full spec.

```
apps/api         @sandboxd/api     the control plane: a generic JSON API (Hono + Zod, OpenAPI at /doc),
                                   the worker tunnel, the attach and preview proxies. Knows nothing of presets.
apps/ui          @sandboxd/ui      the parent-app stand-in: holds the service token and the presets;
                                   serves the xterm.js page; resolves a preset, then calls the API.
apps/worker      @sandboxd/worker  the per-host daemon: Docker driver, PTYs, one outbound tunnel. Zero deps.
packages/core    @sandboxd/core    the wire contract both sides share: messages, framing, ids, log, errors.
```

```bash
bun install
bun run image        # build one image per preset (apps/ui/presets/*/Dockerfile → sandboxd-<name>:latest)
bun run dev          # api on :8080, ui on :8081 and a worker on this machine, one terminal
bun run dev:api      # or one at a time: control plane, state in ./.data, docs at /doc
bun run dev:ui       # UI on :8081, talking to the API with the service token
bun run dev:worker   # worker on this machine; approve it in the UI with the printed code,
                     # or export the same SANDBOXD_JOIN_TOKEN for the api and the worker to skip that
```

Open http://localhost:8081.

## Two ways in

The UI speaks presets. The API speaks images. A parent app can use either.

```bash
# --- through the UI (:8081): presets, no token in the browser ---

# coding agent (default preset when repo is given)
curl -s localhost:8081/sessions -H 'content-type: application/json' \
  -d '{"owner_id":"me","repo":"https://github.com/org/repo.git","prompt":"add tests for src/x.ts"}'

# vs code on a repo, previewed on port 8080 (POST /sessions/:id/preview-token {"owner_id":"me","port":8080})
curl -s localhost:8081/sessions -H 'content-type: application/json' \
  -d '{"owner_id":"me","preset":"vscode","repo":"https://github.com/org/repo.git"}'

# jupyterlab, previewed on port 8888 (kernel websockets go through the preview proxy)
curl -s localhost:8081/sessions -H 'content-type: application/json' \
  -d '{"owner_id":"me","preset":"jupyter","repo":"https://github.com/org/notebooks.git"}'

curl -s localhost:8081/presets    # every preset's fields, image, preview port

# --- straight to the API (:8080): image + command + env, the service token ---

curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","image":"python:3.12","cmd":["python3","-m","http.server","8000"],"env":{"PYTHONUNBUFFERED":"1"}}'

open http://localhost:8080/doc    # the OpenAPI reference (spec at /openapi.json)
```

## Presets are data

`apps/ui/presets/<name>/preset.yaml` declares a preset: its description, image (default
`sandboxd-<name>:latest`), idle timeout, preview port and the request fields it
accepts, each mapped onto one env var the image's `entry.sh` reads. The
`Dockerfile` next to it is the image; `bun run image` builds them all. Adding a preset is
adding a folder. Defaults such as the agent or the model live in the image's entry
script, so an operator overrides them with `SANDBOXD_SANDBOX_ENV_<NAME>` on the API
(e.g. `SANDBOXD_SANDBOX_ENV_MODEL=gpt-5`, `SANDBOXD_SANDBOX_ENV_CODEX_MODEL=gpt-5`).
A malformed preset stops the UI at boot with the file name.

A session is exactly one container: no sidecars, no per-session network. A repo that
needs a database runs it inside the sandbox or points at one outside.

Every host needs the preset images locally (the worker only pulls on a 404): push them to
a registry and set `image:` in the preset for multi-host setups.

## Configuration

API: `SANDBOXD_SERVICE_TOKEN` (default `dev-token` with a warning), `SANDBOXD_PORT` (8080),
`SANDBOXD_PUBLIC_URL`, `SANDBOXD_PREVIEW_DOMAIN`, `SANDBOXD_DB`, `SANDBOXD_SECRET`. Env injected into every sandbox: `SANDBOXD_SANDBOX_ENV_<NAME>=value`, plus the
shorthands `SANDBOXD_LLM_BASE_URL` / `SANDBOXD_LLM_API_KEY` (or plain `LLM_BASE_URL` / `LLM_API_KEY`).

UI: `SANDBOXD_API_URL` (http://localhost:8080), `SANDBOXD_UI_PORT` (8081), `SANDBOXD_SERVICE_TOKEN`
(must match the API's), `SANDBOXD_PRESETS_DIR` (default `apps/ui/presets`), `SANDBOXD_DEFAULT_IMAGE`
(for `custom` sessions without an image; none by default).

Worker: `SANDBOXD_URL` (the API, ws://…), `SANDBOXD_WORKER_NAME`, `SANDBOXD_WORKER_MAX_SESSIONS`,
`SANDBOXD_WORKER_CONFIG`, `SANDBOXD_WORKER_ENTRY`.

Schema changes are not migrated in v1: a db from another version is wiped at boot, with a
warning in the log. Hosts re-enroll on their next hello; sessions are gone.

## Working on it

Bun only, one entrypoint per app. `bun run fmt && bun run lint && bun run typecheck && bun run test`
is the gate; `bun run fallow` reports dead code and import-boundary violations.
