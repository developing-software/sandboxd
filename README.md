# devagents

Run coding agents (Claude Code, Codex, OpenCode) on your own machines, supervised
from a browser terminal, behind one control plane. See `DESIGN.md` for the full spec.

```bash
bun install
bun run image        # build the sandbox image (git, node, bun, claude, codex, opencode)
bun run dev:cp       # control plane on :8080, state in ./.data, dev UI at /dev
bun run dev:agent    # agent on this machine; approve it in the UI with the printed code
```

Open http://localhost:8080/dev. Env for the control plane: `DEVAGENTS_SERVICE_TOKEN`
(default `dev-token` with a warning), `DEVAGENTS_LLM_BASE_URL`, `DEVAGENTS_LLM_API_KEY`,
`DEVAGENTS_DEFAULT_MODEL`, `DEVAGENTS_DEFAULT_AGENT`, `DEVAGENTS_PREVIEW_DOMAIN`, `DEVAGENTS_PUBLIC_URL`.
Env for the agent: `DEVAGENTS_URL`, `DEVAGENTS_AGENT_NAME`, `DEVAGENTS_AGENT_MAX_SESSIONS`, `DEVAGENTS_AGENT_CONFIG`.
