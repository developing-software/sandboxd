// The service catalog: presets/services.yaml, an ordinary compose file. The UI never
// translates it — a picked service is copied into the compose document sent to the API,
// and the API keeps the subset the worker can run. Here it is read to list and to copy.
import { existsSync, readFileSync } from 'node:fs'

export type ComposeService = Record<string, unknown>

/** What GET /services shows: enough for a UI to offer the entry and say what it injects. */
export interface CatalogView {
  name: string
  image: string | null
  ready: { port: number; timeout_s?: number } | null
  sandbox_env: Record<string, string>
}

export class Catalog {
  constructor(private services: Record<string, ComposeService> = {}) {}

  get names(): string[] {
    return Object.keys(this.services)
  }
  get(name: string): ComposeService | undefined {
    return this.services[name]
  }
  list(): CatalogView[] {
    return Object.entries(this.services).map(([name, s]) => {
      const x = isObj(s['x-sandboxd']) ? s['x-sandboxd'] : {}
      return {
        name,
        image: typeof s.image === 'string' ? s.image : null,
        ready: readyOf(x.ready),
        sandbox_env: stringMap(x.sandbox_env),
      }
    })
  }
}

/** Empty when the file does not exist; a malformed file is a boot error naming it. */
export function loadCatalog(file: string): Catalog {
  if (!existsSync(file)) return new Catalog()
  const doc: unknown = Bun.YAML.parse(readFileSync(file, 'utf8'))
  if (!isObj(doc) || !isObj(doc.services))
    throw new Error(`${file}: expected a compose file with a services map`)
  for (const [name, s] of Object.entries(doc.services))
    if (!isObj(s)) throw new Error(`${file}: services.${name} must be a map`)
  return new Catalog(doc.services as Record<string, ComposeService>)
}

const readyOf = (v: unknown): CatalogView['ready'] => {
  if (!isObj(v) || !Number.isInteger(v.port)) return null
  return Number.isInteger(v.timeout_s)
    ? { port: v.port as number, timeout_s: v.timeout_s as number }
    : { port: v.port as number }
}
const stringMap = (v: unknown): Record<string, string> =>
  isObj(v) ? Object.fromEntries(Object.entries(v).map(([k, val]) => [k, String(val)])) : {}
export const isObj = (v: unknown): v is Record<string, unknown> =>
  !!v && typeof v === 'object' && !Array.isArray(v)
