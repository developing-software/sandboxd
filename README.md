# devagents

Run sandboxes on your own machines, supervised from a browser terminal, behind one
control plane. Presets are folders of data in `presets/`: `coding-agent` clones a repo
and runs Claude Code, Codex or OpenCode in it; `vscode` opens the repo in VS Code
(code-server) behind the preview proxy; `jupyter` runs JupyterLab the same way; a
`custom` session is any image + command + env. Sessions can bring sidecar services
(Postgres, Redis, …) from a catalog or from a docker compose file.
See `DESIGN.md` for the full spec.

```bash
bun install
bun run image        # build one image per preset (presets/*/Dockerfile → devagents-<name>:latest)
bun run dev:cp       # control plane on :8080, state in ./.data, dev UI at /dev
bun run dev:agent    # agent on this machine; approve it in the UI with the printed code
```

Open http://localhost:8080/dev.

```bash
# coding agent (default preset when repo is given)
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","repo":"https://github.com/org/repo.git","prompt":"add tests for src/x.ts"}'

# vs code on a repo, previewed on port 8080 (POST /sessions/:id/preview-token {"port":8080})
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","preset":"vscode","repo":"https://github.com/org/repo.git"}'

# jupyterlab, previewed on port 8888 (kernel websockets go through the preview proxy)
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","preset":"jupyter","repo":"https://github.com/org/notebooks.git"}'

# a repo that needs postgres: pick it from the catalog (presets/services.yaml). It starts on a private
# per-session network before the sandbox, and the sandbox gets DATABASE_URL / PG* pointing at it
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","repo":"https://github.com/org/app.git","prompt":"fix the failing migration","setup":"bun install",
       "services":["postgres"]}'

# or hand the CP the repo's own compose file: services with an image become sidecars,
# the app itself (build: .) is skipped because the sandbox is where it runs
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d "$(jq -n --rawfile c docker-compose.yml '{owner_id:"me",repo:"https://github.com/org/app.git",compose:$c}')"

# anything else: image + command + env
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","preset":"custom","image":"python:3.12","cmd":["python3","-m","http.server","8000"],"env":{"PYTHONUNBUFFERED":"1"}}'

curl -s localhost:8080/presets  -H 'authorization: Bearer dev-token'   # every preset's fields, image, preview port
curl -s localhost:8080/services -H 'authorization: Bearer dev-token'   # the service catalog
```

## Presets are data

`presets/<name>/preset.yaml` declares a preset: its description, image (default
`devagents-<name>:latest`), idle timeout, preview port, default services and the
request fields it accepts, each mapped onto one env var the image's `entry.sh` reads.
The `Dockerfile` next to it is the image; `bun run image` builds them all. Adding a
preset is adding a folder. Defaults such as the agent or the model live in the image's
entry script, so an operator overrides them with `DEVAGENTS_SANDBOX_ENV_<NAME>`
(e.g. `DEVAGENTS_SANDBOX_ENV_MODEL=gpt-5`, `DEVAGENTS_SANDBOX_ENV_CODEX_MODEL=gpt-5`).
A malformed preset stops the control plane at boot with the file name.

`presets/services.yaml` is the service catalog: an ordinary compose file
(`docker compose -f presets/services.yaml up` works) whose services a session picks by
name. The `x-devagents` extension adds what compose cannot say: `ready.port` (the TCP
port the daemon waits on), `sandbox_env` (env injected into the sandbox, e.g.
`DATABASE_URL`) and `secret_env`. The same translator handles the `compose` field of
`POST /sessions`: image, environment, command, expose/ports (readiness port) and
depends_on (start order) are kept; laptop concerns (volumes, host ports, restart,
healthcheck) are ignored; keys that would change what runs on the host (privileged,
devices, network_mode, user, entrypoint, …) are refused with a 400 naming the key.

Every agent host needs the preset images locally (the daemon only pulls on a 404):
push them to a registry and set `image:` in the preset for multi-host setups.

Control plane env: `DEVAGENTS_SERVICE_TOKEN` (default `dev-token` with a warning),
`DEVAGENTS_PRESETS_DIR` (default `./presets` of the checkout), `DEVAGENTS_DEFAULT_IMAGE`
(for `custom` sessions without an image; default `devagents-coding-agent:latest`),
`DEVAGENTS_PREVIEW_DOMAIN`, `DEVAGENTS_PUBLIC_URL`, `DEVAGENTS_DB`, `DEVAGENTS_MAX_SERVICES` (8).
Env injected into every sandbox: `DEVAGENTS_SANDBOX_ENV_<NAME>=value`, plus the shorthands
`DEVAGENTS_LLM_BASE_URL` / `DEVAGENTS_LLM_API_KEY` (or plain `LLM_BASE_URL` / `LLM_API_KEY`).
Removed: `DEVAGENTS_DEFAULT_AGENT`, `DEVAGENTS_DEFAULT_MODEL`, `DEVAGENTS_CODEX_MODEL` (use
`DEVAGENTS_SANDBOX_ENV_AGENT` / `_MODEL` / `_CODEX_MODEL`), `DEVAGENTS_JUPYTER_IMAGE`,
`DEVAGENTS_JUPYTER_IDLE_S` (edit `presets/jupyter/preset.yaml`).
Agent env: `DEVAGENTS_URL`, `DEVAGENTS_AGENT_NAME`, `DEVAGENTS_AGENT_MAX_SESSIONS`, `DEVAGENTS_AGENT_CONFIG`, `DEVAGENTS_AGENT_ENTRY`.

Schema changes are not migrated in v1: if the control plane refuses to start, delete `.data/cp.db*`.
