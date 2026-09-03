export interface CpConfig {
  port: number;
  serviceToken: string;
  secret: string;
  publicUrl: string;       // http(s)://cp.example.com  (what browsers use)
  previewDomain: string;   // preview.cp.example.com   (wildcard)
  defaultImage: string;
  /** Operator-level env injected into every sandbox at placement time (never persisted).
   *  From DEVAGENTS_SANDBOX_ENV_<NAME>=value, plus the LLM_BASE_URL / LLM_API_KEY shorthands. */
  sandboxEnv: Record<string, string>;
  /** Defaults for the built-in `coding-agent` preset. */
  codingAgent: { defaultAgent: string; defaultModel: string | null; codexModel: string | null };
  /** Defaults for the built-in `jupyter` preset. */
  jupyter: { image: string; idleTimeoutS: number };
  dbPath: string;
  dev: boolean;
}

const SANDBOX_ENV_PREFIX = "DEVAGENTS_SANDBOX_ENV_";

export function loadConfig(env = process.env): CpConfig {
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
    defaultImage: env.DEVAGENTS_DEFAULT_IMAGE ?? "devagents-sandbox:latest",
    sandboxEnv,
    codingAgent: {
      defaultAgent: env.DEVAGENTS_DEFAULT_AGENT || "claude",
      defaultModel: env.DEVAGENTS_DEFAULT_MODEL || "claude-sonnet-4-6",
      // Codex only speaks the Responses API; through LiteLLM that path is OpenAI-models-only in practice.
      codexModel: env.DEVAGENTS_CODEX_MODEL || null,
    },
    jupyter: {
      image: env.DEVAGENTS_JUPYTER_IMAGE || "quay.io/jupyter/minimal-notebook:latest",
      idleTimeoutS: Number(env.DEVAGENTS_JUPYTER_IDLE_S || 4 * 3600),
    },
    dbPath: env.DEVAGENTS_DB ?? "cp.db",
    dev: env.DEVAGENTS_DEV === "true" || env.DEVAGENTS_DEV === "1",
  };
}
