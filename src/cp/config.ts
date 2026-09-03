// Core control-plane config. Preset-specific defaults live with each preset (presets/<name>/preset.yaml).
import { fileURLToPath } from "node:url";

export type Env = Record<string, string | undefined>;

export interface CpConfig {
  port: number;
  serviceToken: string;
  secret: string;
  publicUrl: string;       // http(s)://cp.example.com  (what browsers use)
  previewDomain: string;   // preview.cp.example.com   (wildcard)
  /** Image for sessions whose preset implies none and whose caller sends none. */
  defaultImage: string;
  /** Directory of presets/<name>/preset.yaml (+ services.yaml). DEVAGENTS_PRESETS_DIR; default = the repo's presets/. */
  presetsDir: string;
  /** Operator-level env injected into every sandbox at placement time (never persisted).
   *  From DEVAGENTS_SANDBOX_ENV_<NAME>=value, plus the LLM_BASE_URL / LLM_API_KEY shorthands. */
  sandboxEnv: Record<string, string>;
  /** Cap on sidecar services per session (DEVAGENTS_MAX_SERVICES, default 8). */
  maxServices: number;
  dbPath: string;
  dev: boolean;
}

const SANDBOX_ENV_PREFIX = "DEVAGENTS_SANDBOX_ENV_";

export function loadConfig(env: Env = process.env): CpConfig {
  let serviceToken = env.DEVAGENTS_SERVICE_TOKEN;
  if (!serviceToken) {
    serviceToken = "dev-token";
    console.warn('WARN  DEVAGENTS_SERVICE_TOKEN is not set; using the default "dev-token". Do not run this way outside local dev.');
  }
  const port = Number(env.DEVAGENTS_PORT ?? 8080);
  const publicUrl = (env.DEVAGENTS_PUBLIC_URL ?? `http://localhost:${port}`).replace(/\/+$/, "");

  const sandboxEnv: Record<string, string> = {};
  // Plain LLM_* are accepted too, since that is what people naturally export.
  const llmBase = (env.DEVAGENTS_LLM_BASE_URL || env.LLM_BASE_URL)?.replace(/\/+$/, "");
  if (llmBase) sandboxEnv.LLM_BASE_URL = llmBase;
  const llmKey = env.DEVAGENTS_LLM_API_KEY || env.LLM_API_KEY;
  if (llmKey) sandboxEnv.LLM_API_KEY = llmKey;
  for (const [k, v] of Object.entries(env)) {
    if (k.startsWith(SANDBOX_ENV_PREFIX) && v !== undefined) sandboxEnv[k.slice(SANDBOX_ENV_PREFIX.length)] = v;
  }

  return {
    port,
    serviceToken,
    secret: env.DEVAGENTS_SECRET ?? serviceToken,
    publicUrl,
    previewDomain: env.DEVAGENTS_PREVIEW_DOMAIN ?? "preview.localhost",
    defaultImage: env.DEVAGENTS_DEFAULT_IMAGE ?? "devagents-coding-agent:latest",
    presetsDir: env.DEVAGENTS_PRESETS_DIR ?? fileURLToPath(new URL("../../presets", import.meta.url)),
    sandboxEnv,
    maxServices: Number(env.DEVAGENTS_MAX_SERVICES || 8),
    dbPath: env.DEVAGENTS_DB ?? "cp.db",
    dev: env.DEVAGENTS_DEV === "true" || env.DEVAGENTS_DEV === "1",
  };
}
