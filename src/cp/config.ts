import { AGENT_KINDS, type AgentKind } from "../protocol/messages.ts";

export interface CpConfig {
  port: number;
  serviceToken: string;
  secret: string;
  publicUrl: string;       // http(s)://cp.example.com  (what browsers use)
  previewDomain: string;   // preview.cp.example.com   (wildcard)
  defaultImage: string;
  defaultAgent: AgentKind;
  defaultModel: string | null;
  // Operator-level gateway defaults; a session may override them. api key stays in process memory only.
  llmBaseUrl: string | null;
  llmApiKey: string | null;
  dbPath: string;
  dev: boolean;
}

export function loadConfig(env = process.env): CpConfig {
  let serviceToken = env.DEVAGENTS_SERVICE_TOKEN;
  if (!serviceToken) {
    serviceToken = "dev-token";
    console.warn('WARN  DEVAGENTS_SERVICE_TOKEN is not set; using the default "dev-token". Do not run this way outside local dev.');
  }
  const port = Number(env.DEVAGENTS_PORT ?? 8080);
  const publicUrl = (env.DEVAGENTS_PUBLIC_URL ?? `http://localhost:${port}`).replace(/\/+$/, "");
  return {
    port,
    serviceToken,
    secret: env.DEVAGENTS_SECRET ?? serviceToken,
    publicUrl,
    previewDomain: env.DEVAGENTS_PREVIEW_DOMAIN ?? "preview.localhost",
    defaultImage: env.DEVAGENTS_DEFAULT_IMAGE ?? "devagents-sandbox:latest",
    defaultAgent: (AGENT_KINDS as string[]).includes(env.DEVAGENTS_DEFAULT_AGENT ?? "") ? (env.DEVAGENTS_DEFAULT_AGENT as AgentKind) : "claude",
    defaultModel: env.DEVAGENTS_DEFAULT_MODEL || "claude-sonnet-4-6",
    // Plain LLM_* are accepted too, since that is what people naturally export.
    llmBaseUrl: (env.DEVAGENTS_LLM_BASE_URL || env.LLM_BASE_URL)?.replace(/\/+$/, "") || null,
    llmApiKey: env.DEVAGENTS_LLM_API_KEY || env.LLM_API_KEY || null,
    dbPath: env.DEVAGENTS_DB ?? "cp.db",
    dev: env.DEVAGENTS_DEV === "true" || env.DEVAGENTS_DEV === "1",
  };
}
