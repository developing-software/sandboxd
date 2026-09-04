// Validation of caller-supplied env maps (session env/secret_env and per-service env).
import { Err } from '@sandboxd/core/errors'

/** Set by the daemon on every PTY; callers may not override them. */
const RESERVED = new Set(['TERM', 'SANDBOXD_SESSION_ID'])
const KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/
const MAX_BYTES = 64 * 1024

export namespace Env {
  export const validate = (name: string, env: unknown): Record<string, string> => {
    if (env === undefined || env === null) return {}
    if (typeof env !== 'object' || Array.isArray(env))
      throw Err.badRequest(`${name} must be an object of strings`)
    let bytes = 0
    for (const [k, v] of Object.entries(env as Record<string, unknown>)) {
      if (!KEY_RE.test(k)) throw Err.badRequest(`${name}: invalid variable name "${k}"`)
      if (RESERVED.has(k)) throw Err.badRequest(`${name}: "${k}" is reserved`)
      if (typeof v !== 'string') throw Err.badRequest(`${name}.${k} must be a string`)
      bytes += k.length + v.length
    }
    if (bytes > MAX_BYTES)
      throw Err.badRequest(
        `${name} exceeds ${MAX_BYTES} bytes; ship large inputs through the repo or the image`,
      )
    return env as Record<string, string>
  }
}
