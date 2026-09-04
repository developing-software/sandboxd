// A runtime Preset from a validated preset.yaml: field validation and env emission.
// Request-time errors are 400s.
import { Err } from '@sandboxd/core/errors'
import type { PresetDoc } from './schema'
import type { Body, Expanded, FieldSpec, Preset } from './types'

export function presetFromDoc(doc: PresetDoc): Preset {
  const { claims, ...info } = doc
  return {
    name: doc.name,
    info,
    ...(claims.length > 0
      ? { claims: (body: Body) => claims.every((c) => present(readPath(body, c))) }
      : {}),
    expand(body: Body): Expanded {
      const env: Record<string, string> = {}
      const secret_env: Record<string, string> = {}
      for (const f of info.fields) {
        const v = fieldValue(f, readPath(body, f.name))
        if (v === undefined) continue
        ;(f.secret ? secret_env : env)[f.env] = v
      }
      const out: Expanded = { env, secret_env }
      if (info.image) out.image = info.image
      if (info.cmd) out.cmd = info.cmd
      if (info.idle_timeout_s !== null) out.idle_timeout_s = info.idle_timeout_s
      return out
    },
  }
}

/** The env string for a field, or undefined when it is absent and has no default. */
function fieldValue(f: FieldSpec, raw: unknown): string | undefined {
  let v = raw
  if (typeof v === 'string' && f.type !== 'bool' && f.type !== 'int') v = v.trim()
  if (v === undefined || v === null || v === '') {
    if (f.required) throw Err.badRequest(`${f.name} is required`)
    return f.default === undefined ? undefined : String(f.default)
  }
  switch (f.type) {
    case 'string':
      if (typeof v !== 'string') throw Err.badRequest(`${f.name} must be a string`)
      return v
    case 'enum':
      if (typeof v !== 'string' || !f.values!.includes(v))
        throw Err.badRequest(`${f.name} must be one of ${f.values!.join(', ')}`)
      return v
    case 'url':
      if (typeof v !== 'string' || !/^https?:\/\//.test(v))
        throw Err.badRequest(`${f.name} must be http(s)`)
      return v.replace(/\/+$/, '')
    case 'int': {
      const lo = f.min ?? Number.MIN_SAFE_INTEGER
      const hi = f.max ?? Number.MAX_SAFE_INTEGER
      if (!Number.isInteger(v) || (v as number) < lo || (v as number) > hi) {
        throw Err.badRequest(
          `${f.name} must be an integer${f.min !== undefined || f.max !== undefined ? ` in ${f.min ?? ''}..${f.max ?? ''}` : ''}`,
        )
      }
      return String(v)
    }
    case 'bool':
      if (typeof v !== 'boolean') throw Err.badRequest(`${f.name} must be true or false`)
      return v ? 'true' : 'false'
  }
}

const present = (v: unknown) =>
  v !== undefined && v !== null && !(typeof v === 'string' && !v.trim())

/** `a.b.c` from a nested body. A non-object on the way is a 400 naming the prefix. */
export function readPath(body: Body, path: string): unknown {
  const parts = path.split('.')
  let cur: unknown = body
  for (let i = 0; i < parts.length; i++) {
    if (cur === undefined || cur === null) return undefined
    if (typeof cur !== 'object' || Array.isArray(cur))
      throw Err.badRequest(`${parts.slice(0, i).join('.')} must be an object`)
    cur = (cur as Record<string, unknown>)[parts[i]!]
  }
  return cur
}
