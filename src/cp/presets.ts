// Presets turn a friendly request shape into the generic {image, cmd, env, secret_env}
// the core understands. The core never looks inside env; the sandbox image does.
import type { CpConfig } from "./config.ts";
import { badRequest } from "../shared/errors.ts";

export const AGENT_KINDS = ["claude", "codex", "opencode", "shell"] as const;
export type AgentKind = (typeof AGENT_KINDS)[number];

export interface JupyterFields {
  /** Optional repo to clone into the notebook root (public, or with secrets.git_token). */
  repo?: string;
  /** Which UI to serve. Default lab. */
  ui?: JupyterUi;
  /** Port Jupyter listens on inside the sandbox; preview it with this port. Default 8888. */
  port?: number;
}

export interface CodingAgentFields {
  repo?: string; prompt?: string; base_branch?: string; branch?: string;
  agent?: AgentKind; model?: string;
  /** Gateway override for this session (e.g. LiteLLM). api_key is a secret; base_url is not. */
  llm?: { base_url?: string; api_key?: string };
  secrets?: { git_token?: string; anthropic_api_key?: string };
}

/** What a preset implies. image/cmd/idle_timeout_s are defaults the caller may override. */
export interface Expanded {
  env: Record<string, string>; secret_env: Record<string, string>;
  image?: string; cmd?: string[]; idle_timeout_s?: number;
}

export type PresetBody = CodingAgentFields & JupyterFields;
export type Preset = (sid: string, body: PresetBody, cfg: CpConfig) => Expanded;

export const JUPYTER_UIS = ["lab", "notebook"] as const;
export type JupyterUi = (typeof JUPYTER_UIS)[number];

/** Runs in the PTY via `bash -lc`. Clones REPO into the notebook root when set,
 *  then execs Jupyter bound on all interfaces so the daemon can dial it from
 *  the bridge network. Auth is off inside the container: the preview cookie
 *  is the gate, and the token would have nowhere to go in the redirect flow.
 *  allow_origin=* because the proxy rewrites Host to localhost while the
 *  browser's Origin is the preview subdomain. */
const JUPYTER_ENTRY = [
  'set -e',
  'root="${JUPYTER_ROOT:-$PWD}"',
  'if [ -n "$REPO" ]; then',
  '  root="$root/$(basename "$REPO" .git)"',
  '  if [ -n "$GIT_TOKEN" ]; then',
  "    printf '#!/bin/sh\\nexec printf %%s \"$GIT_TOKEN\"\\n' > /tmp/askpass && chmod +x /tmp/askpass",
  '    export GIT_ASKPASS=/tmp/askpass GIT_TERMINAL_PROMPT=0',
  '  fi',
  '  [ -d "$root/.git" ] || git clone --depth 1 "$REPO" "$root"',
  'fi',
  'exec jupyter "$JUPYTER_UI" --ip=0.0.0.0 --port="$JUPYTER_PORT" --no-browser',
  '  --ServerApp.root_dir="$root" --IdentityProvider.token= --ServerApp.password=',
  "  --ServerApp.allow_remote_access=True --ServerApp.allow_origin='*' --ServerApp.trust_xheaders=True",
].join("\n").replace(/\n  --/g, " --");

export const PRESETS: Record<string, Preset> = {
  /** Fresh clone on its own branch, then one of claude|codex|opencode|shell in the PTY.
   *  Everything here is consumed by images/entry.sh. */
  "coding-agent"(sid, b, cfg) {
    if (!b.repo || typeof b.repo !== "string") throw badRequest("repo is required");
    const prompt = b.prompt ?? "";
    if (typeof prompt !== "string") throw badRequest("prompt must be a string");
    const agent = (b.agent ?? (prompt.trim() ? cfg.codingAgent.defaultAgent : "shell")) as AgentKind;
    if (!AGENT_KINDS.includes(agent)) throw badRequest(`agent must be one of ${AGENT_KINDS.join(", ")}`);
    const model = b.model?.trim() || (agent === "codex" ? cfg.codingAgent.codexModel ?? cfg.codingAgent.defaultModel : cfg.codingAgent.defaultModel);
    const llmBase = (b.llm?.base_url?.trim() || "").replace(/\/+$/, "");
    if (llmBase && !/^https?:\/\//.test(llmBase)) throw badRequest("llm.base_url must be http(s)");

    const env: Record<string, string> = {
      REPO: b.repo, BRANCH: b.branch?.trim() || `devagents/${sid}`, BASE_BRANCH: b.base_branch?.trim() || "",
      PROMPT: prompt, AGENT: agent, MODEL: model ?? "",
    };
    if (llmBase) env.LLM_BASE_URL = llmBase;
    const secret_env: Record<string, string> = {};
    if (b.secrets?.git_token) secret_env.GIT_TOKEN = b.secrets.git_token;
    if (b.secrets?.anthropic_api_key) secret_env.ANTHROPIC_API_KEY = b.secrets.anthropic_api_key;
    if (b.llm?.api_key) secret_env.LLM_API_KEY = b.llm.api_key;
    return { env, secret_env };
  },
  /** JupyterLab (or classic Notebook) on a stock jupyter/docker-stacks image, previewed on `port`.
   *  Notebook traffic goes through the preview proxy (HTTP + kernel WebSockets). Idle defaults to
   *  4 h because preview traffic does not count as activity (design decision 10). */
  jupyter(_sid, b, cfg) {
    const ui = (b.ui ?? "lab") as JupyterUi;
    if (!JUPYTER_UIS.includes(ui)) throw badRequest(`ui must be one of ${JUPYTER_UIS.join(", ")}`);
    const port = b.port ?? 8888;
    if (!Number.isInteger(port) || port < 1 || port > 65535) throw badRequest("port must be an integer in 1..65535");
    if (b.repo !== undefined && (typeof b.repo !== "string" || !b.repo.trim())) throw badRequest("repo must be a non-empty string");
    const env: Record<string, string> = { JUPYTER_UI: ui, JUPYTER_PORT: String(port), REPO: b.repo?.trim() ?? "" };
    const secret_env: Record<string, string> = {};
    if (b.secrets?.git_token) secret_env.GIT_TOKEN = b.secrets.git_token;
    return { env, secret_env, image: cfg.jupyter.image, cmd: ["bash", "-lc", JUPYTER_ENTRY], idle_timeout_s: cfg.jupyter.idleTimeoutS };
  },
  /** Nothing implied. The caller supplies image/cmd/env and owns the contract with the image. */
  custom() { return { env: {}, secret_env: {} }; },
};

export const PRESET_NAMES = Object.keys(PRESETS);

/** Set by the daemon on every PTY; callers may not override them. */
const RESERVED = new Set(["TERM", "DEVAGENTS_SESSION_ID"]);
const KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const MAX_ENV_BYTES = 64 * 1024;

export function validateEnv(name: string, env: unknown): Record<string, string> {
  if (env === undefined || env === null) return {};
  if (typeof env !== "object" || Array.isArray(env)) throw badRequest(`${name} must be an object of strings`);
  let bytes = 0;
  for (const [k, v] of Object.entries(env as Record<string, unknown>)) {
    if (!KEY_RE.test(k)) throw badRequest(`${name}: invalid variable name "${k}"`);
    if (RESERVED.has(k)) throw badRequest(`${name}: "${k}" is reserved`);
    if (typeof v !== "string") throw badRequest(`${name}.${k} must be a string`);
    bytes += k.length + v.length;
  }
  if (bytes > MAX_ENV_BYTES) throw badRequest(`${name} exceeds ${MAX_ENV_BYTES} bytes; ship large inputs through the repo or the image`);
  return env as Record<string, string>;
}
