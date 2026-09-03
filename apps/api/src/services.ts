// Sidecar services: the caller's declarations split into the persisted half and the
// secret half, then merged with what a compose document adds. Catalog names are a UI
// concern (apps/ui); the API only ever sees services spelled out or a compose document.
import type { ServiceDecl } from './store'
import { badRequest } from '@sandboxd/core/errors'
import type { ServiceInput } from './schema'

/** DNS label on the session network; also the hostname the sandbox uses. */
export const NAME_RE = /^[a-z][a-z0-9-]{0,30}$/
/** The sandbox's own alias on the network. */
export const SANDBOX_ALIAS = 'sandbox'
export const DEFAULT_READY_TIMEOUT_S = 60
export const MAX_READY_TIMEOUT_S = 600

/** Services after validation. `secrets` and `sandbox_env` are keyed by service name. */
export interface ValidatedServices {
  decls: ServiceDecl[]
  /** Secret env per service; memory-only. */
  secrets: Record<string, Record<string, string>>
  /** Env each service asks to inject into the sandbox (e.g. DATABASE_URL). Only compose can say this. */
  sandbox_env: Record<string, Record<string, string>>
}

export const noServices = (): ValidatedServices => ({
  decls: [],
  secrets: {},
  sandbox_env: {},
})

/** The caller's `services` array, already shaped by the schema, with secrets split out. */
export function fromDecls(items: ServiceInput[]): ValidatedServices {
  const out = noServices()
  items.forEach((s, i) => {
    if (out.decls.some((x) => x.name === s.name))
      throw badRequest(`services[${i}].name "${s.name}" is duplicated`)
    out.decls.push({ name: s.name, image: s.image, env: s.env, cmd: s.cmd, ready: s.ready })
    if (Object.keys(s.secret_env).length > 0) out.secrets[s.name] = s.secret_env
  })
  return out
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

// The three below are what compose.ts uses to validate what a compose document says.

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
