// Control-plane config. The API is generic: it knows images, commands and env, never
// presets. Those live in apps/ui.
export type Env = Record<string, string | undefined>

export interface CpConfig {
  port: number
  serviceToken: string
  secret: string
  /** http(s)://api.example.com — what browsers are told to connect to. */
  publicUrl: string
  /** preview.api.example.com — wildcard. */
  previewDomain: string
  /** Operator-level env injected into every sandbox at placement time (never persisted).
   *  From SANDBOXD_SANDBOX_ENV_<NAME>=value, plus the LLM_BASE_URL / LLM_API_KEY shorthands. */
  sandboxEnv: Record<string, string>
  /** Cap on sidecar services per session (SANDBOXD_MAX_SERVICES, default 8). */
  maxServices: number
  /** SANDBOXD_JOIN_TOKEN: a hello carrying it is approved without the printed code. null = code only. */
  joinToken: string | null
  dbPath: string
}

const SANDBOX_ENV_PREFIX = 'SANDBOXD_SANDBOX_ENV_'

export function loadConfig(env: Env = process.env): CpConfig {
  let serviceToken = env.SANDBOXD_SERVICE_TOKEN
  if (!serviceToken) {
    serviceToken = 'dev-token'
    console.warn(
      'WARN  SANDBOXD_SERVICE_TOKEN is not set; using the default "dev-token". Do not run this way outside local dev.',
    )
  }
  const port = Number(env.SANDBOXD_PORT ?? 8080)
  const publicUrl = (env.SANDBOXD_PUBLIC_URL ?? `http://localhost:${port}`).replace(/\/+$/, '')

  const sandboxEnv: Record<string, string> = {}
  // Plain LLM_* are accepted too, since that is what people naturally export.
  const llmBase = (env.SANDBOXD_LLM_BASE_URL || env.LLM_BASE_URL)?.replace(/\/+$/, '')
  if (llmBase) sandboxEnv.LLM_BASE_URL = llmBase
  const llmKey = env.SANDBOXD_LLM_API_KEY || env.LLM_API_KEY
  if (llmKey) sandboxEnv.LLM_API_KEY = llmKey
  for (const [k, v] of Object.entries(env)) {
    if (k.startsWith(SANDBOX_ENV_PREFIX) && v !== undefined)
      sandboxEnv[k.slice(SANDBOX_ENV_PREFIX.length)] = v
  }

  return {
    port,
    serviceToken,
    secret: env.SANDBOXD_SECRET ?? serviceToken,
    publicUrl,
    previewDomain: env.SANDBOXD_PREVIEW_DOMAIN ?? 'preview.localhost',
    sandboxEnv,
    maxServices: Number(env.SANDBOXD_MAX_SERVICES || 8),
    joinToken: env.SANDBOXD_JOIN_TOKEN || null,
    dbPath: env.SANDBOXD_DB ?? 'cp.db',
  }
}
