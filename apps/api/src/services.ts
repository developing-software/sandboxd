// Sidecar services: the caller's declarations split into the persisted half and the
// secret half, then merged with what a compose document adds. Catalog names are a UI
// concern (apps/ui); the API only ever sees services spelled out or a compose document.
import type { ServiceDecl } from './store'
import { Err } from '@sandboxd/core/errors'
import type { ServiceInput } from './schema'

export namespace Service {
  /** DNS label on the session network; also the hostname the sandbox uses. */
  export const NAME_RE = /^[a-z][a-z0-9-]{0,30}$/
  /** The sandbox's own alias on the network. */
  export const ALIAS = 'sandbox'
  export const DEFAULT_TIMEOUT_S = 60
  export const MAX_TIMEOUT_S = 600

  /** Services after validation. `secrets` and `sandbox_env` are keyed by service name. */
  export interface List {
    decls: ServiceDecl[]
    /** Secret env per service; memory-only. */
    secrets: Record<string, Record<string, string>>
    /** Env each service asks to inject into the sandbox (e.g. DATABASE_URL). Only compose can say this. */
    sandbox_env: Record<string, Record<string, string>>
  }

  export const none = (): List => ({ decls: [], secrets: {}, sandbox_env: {} })

  /** The caller's `services` array, already shaped by the schema, with secrets split out. */
  export const from = (items: ServiceInput[]): List => {
    const out = none()
    items.forEach((s, i) => {
      if (out.decls.some((x) => x.name === s.name))
        throw Err.badRequest(`services[${i}].name "${s.name}" is duplicated`)
      out.decls.push({ name: s.name, image: s.image, env: s.env, cmd: s.cmd, ready: s.ready })
      if (Object.keys(s.secret_env).length > 0) out.secrets[s.name] = s.secret_env
    })
    return out
  }

  /** Concatenate the lists from every source; names must be unique across them. */
  export const merge = (parts: List[], max: number): List => {
    const out = none()
    for (const p of parts) {
      for (const d of p.decls) {
        if (out.decls.some((x) => x.name === d.name))
          throw Err.badRequest(`services: name "${d.name}" is duplicated`)
        out.decls.push(d)
        if (p.secrets[d.name]) out.secrets[d.name] = p.secrets[d.name]!
        if (p.sandbox_env[d.name]) out.sandbox_env[d.name] = p.sandbox_env[d.name]!
      }
    }
    if (out.decls.length > max) throw Err.badRequest(`services: at most ${max} per session`)
    return out
  }

  /** All `sandbox_env` maps folded into one, in service order (later services win). */
  export const sandboxEnv = (s: List): Record<string, string> => {
    const out: Record<string, string> = {}
    for (const d of s.decls) Object.assign(out, s.sandbox_env[d.name] ?? {})
    return out
  }

  // The two below are what compose.ts uses to validate what a compose document says.

  export const name = (at: string, raw: unknown): string => {
    if (typeof raw !== 'string' || !NAME_RE.test(raw))
      throw Err.badRequest(`${at}.name must match ${NAME_RE}`)
    if (raw === ALIAS) throw Err.badRequest(`${at}.name "${ALIAS}" is reserved`)
    return raw
  }

  export const ready = (at: string, raw: unknown): ServiceDecl['ready'] => {
    if (raw === undefined || raw === null) return null
    if (typeof raw !== 'object' || Array.isArray(raw))
      throw Err.badRequest(`${at}.ready must be an object`)
    const { port, timeout_s } = raw as { port?: unknown; timeout_s?: unknown }
    if (!Number.isInteger(port) || (port as number) < 1 || (port as number) > 65535)
      throw Err.badRequest(`${at}.ready.port must be 1-65535`)
    const t = timeout_s ?? DEFAULT_TIMEOUT_S
    if (!Number.isInteger(t) || (t as number) < 1 || (t as number) > MAX_TIMEOUT_S)
      throw Err.badRequest(`${at}.ready.timeout_s must be 1-${MAX_TIMEOUT_S}`)
    return { port: port as number, timeout_s: t as number }
  }
}
