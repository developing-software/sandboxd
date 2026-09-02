// Presets turn a friendly request shape into the generic {image, cmd, env, secret_env}
// the core understands. The core never looks inside env; the sandbox image does.
import type { CpConfig } from "./config.ts";
import { badRequest } from "../shared/errors.ts";

export const AGENT_KINDS = ["claude", "codex", "opencode", "shell"] as const;
export type AgentKind = (typeof AGENT_KINDS)[number];

export interface CodingAgentFields {
  repo?: string; prompt?: string; base_branch?: string; branch?: string;
  agent?: AgentKind; model?: string;
  /** Gateway override for this session (e.g. LiteLLM). api_key is a secret; base_url is not. */
  llm?: { base_url?: string; api_key?: string };
  secrets?: { git_token?: string; anthropic_api_key?: string };
}

export interface Expanded { env: Record<string, string>; secret_env: Record<string, string> }

export type Preset = (sid: string, body: CodingAgentFields, cfg: CpConfig) => Expanded;

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
