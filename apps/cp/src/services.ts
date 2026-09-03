// Sidecar services: validation of the `services` request field, catalog
// references, and merging the lists a session gets from its preset, from the
// caller and from a compose document. The CP checks shape and splits secrets
// out; it never looks inside images or env.
import type { ServiceDecl } from './store'
import { badRequest } from './errors'
import { validateEnv } from './env'

/** DNS label on the session network; also the hostname the sandbox uses. */
export const NAME_RE = /^[a-z][a-z0-9-]{0,30}$/
/** The sandbox's own alias on the network. */
export const SANDBOX_ALIAS = 'sandbox'
const DEFAULT_READY_TIMEOUT_S = 60
const MAX_READY_TIMEOUT_S = 600

/** Services after validation. `secrets` and `sandbox_env` are keyed by service name. */
export interface ValidatedServices {
  decls: ServiceDecl[]
  /** Secret env per service; memory-only. */
  secrets: Record<string, Record<string, string>>
  /** Env each service asks to inject into the sandbox (e.g. DATABASE_URL). */
  sandbox_env: Record<string, Record<string, string>>
}

export const noServices = (): ValidatedServices => ({
  decls: [],
  secrets: {},
  sandbox_env: {},
})

/** A catalog entry: a validated service the caller can pick by name. */
export interface CatalogEntry {
  decl: ServiceDecl
  secret_env: Record<string, string>
  sandbox_env: Record<string, string>
}
export interface ServiceCatalog {
  readonly names: string[]
  get(name: string): CatalogEntry | undefined
}
export const EMPTY_CATALOG: ServiceCatalog = { names: [], get: () => undefined }

/** `services: ["postgres"]` or `{use: "postgres", name: "db", env: {...}}`. */
export interface ServiceRef {
  use: string
  name?: string
  env?: Record<string, string>
  secret_env?: Record<string, string>
}

export interface ServiceInput {
  name?: unknown
  image?: unknown
  env?: unknown
  secret_env?: unknown
  ready?: unknown
  cmd?: unknown
  use?: unknown
}

/** The caller's `services` array: inline declarations and catalog references, in order. */
export function validateServices(
  raw: unknown,
  catalog: ServiceCatalog = EMPTY_CATALOG,
): ValidatedServices {
  if (raw === undefined || raw === null) return noServices()
  if (!Array.isArray(raw)) throw badRequest('services must be an array')
  const out = noServices()
  raw.forEach((item: unknown, i) => {
    const at = `services[${i}]`
    if (typeof item === 'string') return add(out, resolveRef({ use: item }, catalog, at), at)
    if (!item || typeof item !== 'object' || Array.isArray(item))
      throw badRequest(`${at} must be an object`)
    const s = item as ServiceInput
    if (s.use !== undefined) {
      if (typeof s.use !== 'string') throw badRequest(`${at}.use must be a string`)
      if (s.name !== undefined && typeof s.name !== 'string')
        throw badRequest(`${at}.name must be a string`)
      return add(
        out,
        resolveRef(
          {
            use: s.use,
            name: s.name as string | undefined,
            env: validateEnv(`${at}.env`, s.env),
            secret_env: validateEnv(`${at}.secret_env`, s.secret_env),
          },
          catalog,
          at,
        ),
        at,
      )
    }
    const name = validateName(at, s.name)
    if (typeof s.image !== 'string' || !s.image.trim())
      throw badRequest(`${at}.image is required`)
    const decl: ServiceDecl = {
      name,
      image: s.image.trim(),
      env: validateEnv(`${at}.env`, s.env),
      cmd: validateCmd(at, s.cmd),
      ready: validateReady(at, s.ready),
    }
    add(
      out,
      { decl, secret_env: validateEnv(`${at}.secret_env`, s.secret_env), sandbox_env: {} },
      at,
    )
  })
  return out
}

/** A catalog entry with the reference's overrides applied. */
export function resolveRef(
  ref: ServiceRef,
  catalog: ServiceCatalog,
  at: string,
): CatalogEntry {
  const entry = catalog.get(ref.use)
  if (!entry)
    throw badRequest(
      `${at}: unknown service "${ref.use}"${catalog.names.length ? ` (catalog: ${catalog.names.join(', ')})` : ' (the catalog is empty)'}`,
    )
  const name = ref.name === undefined ? entry.decl.name : validateName(at, ref.name)
  return {
    decl: { ...entry.decl, name, env: { ...entry.decl.env, ...ref.env } },
    secret_env: { ...entry.secret_env, ...ref.secret_env },
    sandbox_env: entry.sandbox_env,
  }
}

/** Concatenate the lists from every source; names must be unique across them. */
export function mergeServices(parts: ValidatedServices[], max: number): ValidatedServices {
  const out = noServices()
  for (const p of parts) {
    for (const d of p.decls) {
      if (out.decls.some((x) => x.name === d.name))
        throw badRequest(`services: name "${d.name}" is duplicated`)
      out.decls.push(d)
      if (p.secrets[d.name]) out.secrets[d.name] = p.secrets[d.name]!
      if (p.sandbox_env[d.name]) out.sandbox_env[d.name] = p.sandbox_env[d.name]!
    }
  }
  if (out.decls.length > max) throw badRequest(`services: at most ${max} per session`)
  return out
}

/** All `sandbox_env` maps folded into one, in service order (later services win). */
export function sandboxEnvOf(s: ValidatedServices): Record<string, string> {
  const out: Record<string, string> = {}
  for (const d of s.decls) Object.assign(out, s.sandbox_env[d.name] ?? {})
  return out
}

function add(out: ValidatedServices, e: CatalogEntry, at: string) {
  if (out.decls.some((x) => x.name === e.decl.name))
    throw badRequest(`${at}.name "${e.decl.name}" is duplicated`)
  out.decls.push(e.decl)
  if (Object.keys(e.secret_env).length) out.secrets[e.decl.name] = e.secret_env
  if (Object.keys(e.sandbox_env).length) out.sandbox_env[e.decl.name] = e.sandbox_env
}

export function validateName(at: string, name: unknown): string {
  if (typeof name !== 'string' || !NAME_RE.test(name))
    throw badRequest(`${at}.name must match ${NAME_RE}`)
  if (name === SANDBOX_ALIAS) throw badRequest(`${at}.name "${SANDBOX_ALIAS}" is reserved`)
  return name
}

export function validateCmd(at: string, cmd: unknown): string[] | null {
  if (cmd === undefined || cmd === null) return null
  if (!Array.isArray(cmd) || cmd.length === 0 || !cmd.every((c) => typeof c === 'string'))
    throw badRequest(`${at}.cmd must be a non-empty array of strings`)
  return cmd as string[]
}

export function validateReady(at: string, ready: unknown): ServiceDecl['ready'] {
  if (ready === undefined || ready === null) return null
  if (typeof ready !== 'object' || Array.isArray(ready))
    throw badRequest(`${at}.ready must be an object`)
  const { port, timeout_s } = ready as { port?: unknown; timeout_s?: unknown }
  if (!Number.isInteger(port) || (port as number) < 1 || (port as number) > 65535)
    throw badRequest(`${at}.ready.port must be 1-65535`)
  const t = timeout_s ?? DEFAULT_READY_TIMEOUT_S
  if (!Number.isInteger(t) || (t as number) < 1 || (t as number) > MAX_READY_TIMEOUT_S)
    throw badRequest(`${at}.ready.timeout_s must be 1-${MAX_READY_TIMEOUT_S}`)
  return { port: port as number, timeout_s: t as number }
}
