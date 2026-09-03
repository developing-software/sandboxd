# devagents

Run sandboxes on your own machines, supervised from a browser terminal, behind one
control plane. The built-in `coding-agent` preset clones a repo and runs Claude Code,
Codex or OpenCode in it; `jupyter` runs JupyterLab behind the preview proxy;
a `custom` session is any image + command + env.
See `DESIGN.md` for the full spec.

```bash
bun install
bun run image        # build the sandbox image (git, node, bun, claude, codex, opencode)
bun run dev:cp       # control plane on :8080, state in ./.data, dev UI at /dev
bun run dev:agent    # agent on this machine; approve it in the UI with the printed code
```

Open http://localhost:8080/dev.

```bash
# coding agent (default preset when repo is given)
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","repo":"https://github.com/org/repo.git","prompt":"add tests for src/x.ts"}'

# jupyterlab, previewed on port 8888 (kernel websockets go through the preview proxy)
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","preset":"jupyter","repo":"https://github.com/org/notebooks.git"}'

# anything else: image + command + env
curl -s localhost:8080/sessions -H 'authorization: Bearer dev-token' -H 'content-type: application/json' \
  -d '{"owner_id":"me","preset":"custom","image":"python:3.12","cmd":["python3","-m","http.server","8000"],"env":{"PYTHONUNBUFFERED":"1"}}'
```

Control plane env: `DEVAGENTS_SERVICE_TOKEN` (default `dev-token` with a warning),
`DEVAGENTS_DEFAULT_IMAGE`, `DEVAGENTS_PREVIEW_DOMAIN`, `DEVAGENTS_PUBLIC_URL`, `DEVAGENTS_DB`.
Env injected into every sandbox: `DEVAGENTS_SANDBOX_ENV_<NAME>=value`, plus the shorthands
`DEVAGENTS_LLM_BASE_URL` / `DEVAGENTS_LLM_API_KEY` (or plain `LLM_BASE_URL` / `LLM_API_KEY`).
Coding-agent preset defaults: `DEVAGENTS_DEFAULT_AGENT`, `DEVAGENTS_DEFAULT_MODEL`, `DEVAGENTS_CODEX_MODEL`.
Jupyter preset defaults: `DEVAGENTS_JUPYTER_IMAGE` (any jupyter/docker-stacks image), `DEVAGENTS_JUPYTER_IDLE_S` (4 h;
preview traffic does not count as activity, so the terminal idle timer is what ends the session).
Agent env: `DEVAGENTS_URL`, `DEVAGENTS_AGENT_NAME`, `DEVAGENTS_AGENT_MAX_SESSIONS`, `DEVAGENTS_AGENT_CONFIG`, `DEVAGENTS_AGENT_ENTRY`.

Schema changes are not migrated in v1: if the control plane refuses to start, delete `.data/cp.db*`.
