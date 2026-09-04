// Catalog names and references become compose services; the API translates compose. A
// caller can also spell a sidecar out in full, and that goes to the API untouched.
import { Err } from '@sandboxd/core/errors'
import { Json } from '@sandboxd/core/json'
import type { Catalog, ComposeService } from './presets/catalog'

export namespace Service {
  /** `services: ["postgres"]` or `{use: "postgres", name: "db", env: {...}}`. */
  export interface Ref {
    use: string
    name?: string
    env?: Record<string, string>
    secret_env?: Record<string, string>
  }

  /** Compose services by name, the shape the API is handed. */
  export type Map = Record<string, ComposeService>

  /** A catalog entry copied under the reference's name, with its overrides applied. */
  export const resolve = (
    ref: Ref,
    catalog: Catalog,
    at: string,
  ): [string, ComposeService] => {
    const entry = catalog.get(ref.use)
    if (!entry) {
      const have =
        catalog.names.length > 0
          ? ` (catalog: ${catalog.names.join(', ')})`
          : ' (the catalog is empty)'
      throw Err.badRequest(`${at}: unknown service "${ref.use}"${have}`)
    }
    const out: ComposeService = { ...entry }
    if (ref.env) out.environment = { ...environmentOf(entry.environment), ...ref.env }
    if (ref.secret_env) {
      const x = Json.isObj(entry['x-sandboxd']) ? entry['x-sandboxd'] : {}
      const secret = Json.isObj(x.secret_env) ? x.secret_env : {}
      out['x-sandboxd'] = { ...x, secret_env: { ...secret, ...ref.secret_env } }
    }
    return [ref.name ?? ref.use, out]
  }

  /** The caller's `services`: names and references are resolved here, full declarations pass through. */
  export const split = (
    raw: unknown,
    catalog: Catalog,
  ): { inline: unknown[]; compose: Map } => {
    if (raw === undefined || raw === null) return { inline: [], compose: {} }
    if (!Array.isArray(raw)) throw Err.badRequest('services must be an array')
    const inline: unknown[] = []
    const compose: Map = {}
    raw.forEach((item: unknown, i) => {
      const at = `services[${i}]`
      const ref = typeof item === 'string' ? { use: item } : refOf(item, at)
      if (!ref) {
        inline.push(item)
        return
      }
      const [name, svc] = resolve(ref, catalog, at)
      if (compose[name]) throw Err.badRequest(`${at}.name "${name}" is duplicated`)
      compose[name] = svc
    })
    return { inline, compose }
  }

  /** One compose document from the preset's defaults, the caller's picks and the caller's own file. */
  export const merge = (parts: Map[], own: unknown): unknown => {
    const picked: Map = {}
    for (const p of parts)
      for (const [name, svc] of Object.entries(p)) {
        if (picked[name]) throw Err.badRequest(`services: name "${name}" is duplicated`)
        picked[name] = svc
      }
    const doc = parseOwn(own)
    if (Object.keys(picked).length === 0) return doc
    if (!doc) return { services: picked }
    if (!Json.isObj(doc.services)) throw Err.badRequest('compose must have a services map')
    for (const name of Object.keys(picked))
      if (name in doc.services) throw Err.badRequest(`services: name "${name}" is duplicated`)
    return { ...doc, services: { ...picked, ...doc.services } }
  }

  /** A map that must already be all strings. The coercing twin lives in the catalog. */
  export const strings = (v: unknown, at: string): Record<string, string> => {
    if (!Json.isObj(v)) throw Err.badRequest(`${at} must be an object of strings`)
    for (const [k, val] of Object.entries(v))
      if (typeof val !== 'string') throw Err.badRequest(`${at}.${k} must be a string`)
    return v as Record<string, string>
  }
}

function refOf(item: unknown, at: string): Service.Ref | null {
  if (!Json.isObj(item) || item.use === undefined) return null
  if (typeof item.use !== 'string') throw Err.badRequest(`${at}.use must be a string`)
  if (item.name !== undefined && typeof item.name !== 'string')
    throw Err.badRequest(`${at}.name must be a string`)
  const ref: Service.Ref = { use: item.use }
  if (item.name !== undefined) ref.name = item.name
  if (item.env !== undefined) ref.env = Service.strings(item.env, `${at}.env`)
  if (item.secret_env !== undefined)
    ref.secret_env = Service.strings(item.secret_env, `${at}.secret_env`)
  return ref
}

/** Compose's `environment` in either spelling, as a map. */
function environmentOf(v: unknown): Record<string, unknown> {
  if (Array.isArray(v))
    return Object.fromEntries(
      v.map((line) => {
        const s = String(line)
        const i = s.indexOf('=')
        return i < 0 ? [s, ''] : [s.slice(0, i), s.slice(i + 1)]
      }),
    )
  return Json.isObj(v) ? v : {}
}

function parseOwn(own: unknown): Record<string, unknown> | undefined {
  if (own === undefined || own === null) return undefined
  if (Json.isObj(own)) return own
  if (typeof own !== 'string') throw Err.badRequest('compose must be a compose document')
  let doc: unknown
  try {
    doc = Bun.YAML.parse(own)
  } catch (e) {
    throw Err.badRequest(`compose: ${(e as Error).message}`)
  }
  if (!Json.isObj(doc)) throw Err.badRequest('compose must be a compose document')
  return doc
}
