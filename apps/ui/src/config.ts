// UI config. This app is the parent-app stand-in: it holds the service token, the
// presets and the catalog, and talks to the API on the user's behalf.
import { fileURLToPath } from 'node:url'

export type Vars = Record<string, string | undefined>

export interface UiConfig {
  port: number
  /** The control plane, e.g. http://localhost:8080. */
  apiUrl: string
  serviceToken: string
  /** Directory of <name>/preset.yaml (+ services.yaml). SANDBOXD_PRESETS_DIR; default = this app's presets/. */
  presetsDir: string
  /** Image for sessions whose preset implies none and whose caller sends none. */
  defaultImage: string | null
}

export function loadConfig(env: Vars = process.env): UiConfig {
  let serviceToken = env.SANDBOXD_SERVICE_TOKEN
  if (!serviceToken) {
    serviceToken = 'dev-token'
    console.warn('WARN  SANDBOXD_SERVICE_TOKEN is not set; using the default "dev-token".')
  }
  return {
    port: Number(env.SANDBOXD_UI_PORT ?? 8081),
    apiUrl: (env.SANDBOXD_API_URL ?? 'http://localhost:8080').replace(/\/+$/, ''),
    serviceToken,
    presetsDir:
      env.SANDBOXD_PRESETS_DIR ?? fileURLToPath(new URL('../presets', import.meta.url)),
    defaultImage: env.SANDBOXD_DEFAULT_IMAGE ?? null,
  }
}
