// A runtime Preset from a validated preset.yaml: field validation, env emission
// and catalog service resolution. Request-time errors are 400s.
import { badRequest } from '../errors'
import {
  resolveRef,
  noServices,
  type ServiceCatalog,
  type ValidatedServices,
} from '../services'
import type { PresetDoc } from './schema'
import type { Body, Expanded, FieldSpec, Preset } from './types'

export function presetFromDoc(doc: PresetDoc, catalog: ServiceCatalog): Preset {
  for (const r of doc.serviceRefs) {
    if (!catalog.get(r.use))
      throw new Error(
        `services: unknown catalog service "${r.use}"${catalog.names.length ? ` (catalog: ${catalog.names.join(', ')})` : ' (no presets/services.yaml)'}`,
      )
  }
  const { claims, serviceRefs, ...info } = doc
  return {
    name: doc.name,
    info,
    ...(claims.length
      ? { claims: (body: Body) => claims.every((c) => present(readPath(body, c))) }
      : {}),
    expand(_sid: string, body: Body): Expanded {
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
      if (serviceRefs.length) out.services = resolveAll(serviceRefs, catalog)
      return out
    },
  }
}

function resolveAll(
  refs: PresetDoc['serviceRefs'],
  catalog: ServiceCatalog,
): ValidatedServices {
  const out = noServices()
  refs.forEach((r, i) => {
    const e = resolveRef(r, catalog, `preset services[${i}]`)
    out.decls.push(e.decl)
    if (Object.keys(e.secret_env).length) out.secrets[e.decl.name] = e.secret_env
    if (Object.keys(e.sandbox_env).length) out.sandbox_env[e.decl.name] = e.sandbox_env
  })
  return out
}

/** The env string for a field, or undefined when it is absent and has no default. */
function fieldValue(f: FieldSpec, raw: unknown): string | undefined {
  let v = raw
  if (typeof v === 'string' && f.type !== 'bool' && f.type !== 'int') v = v.trim()
  if (v === undefined || v === null || v === '') {
    if (f.required) throw badRequest(`${f.name} is required`)
    return f.default === undefined ? undefined : String(f.default)
  }
  switch (f.type) {
    case 'string':
      if (typeof v !== 'string') throw badRequest(`${f.name} must be a string`)
      return v
    case 'enum':
      if (typeof v !== 'string' || !f.values!.includes(v))
        throw badRequest(`${f.name} must be one of ${f.values!.join(', ')}`)
      return v
    case 'url':
      if (typeof v !== 'string' || !/^https?:\/\//.test(v))
        throw badRequest(`${f.name} must be http(s)`)
      return v.replace(/\/+$/, '')
    case 'int': {
      const lo = f.min ?? Number.MIN_SAFE_INTEGER,
        hi = f.max ?? Number.MAX_SAFE_INTEGER
      if (!Number.isInteger(v) || (v as number) < lo || (v as number) > hi) {
        throw badRequest(
          `${f.name} must be an integer${f.min !== undefined || f.max !== undefined ? ` in ${f.min ?? ''}..${f.max ?? ''}` : ''}`,
        )
      }
      return String(v)
    }
    case 'bool':
      if (typeof v !== 'boolean') throw badRequest(`${f.name} must be true or false`)
      return v ? 'true' : 'false'
  }
}

const present = (v: unknown) =>
  v !== undefined && v !== null && !(typeof v === 'string' && !v.trim())

/** `a.b.c` from a nested body. A non-object on the way is a 400 naming the prefix. */
function readPath(body: Body, path: string): unknown {
  const parts = path.split('.')
  let cur: unknown = body
  for (let i = 0; i < parts.length; i++) {
    if (cur === undefined || cur === null) return undefined
    if (typeof cur !== 'object' || Array.isArray(cur))
      throw badRequest(`${parts.slice(0, i).join('.')} must be an object`)
    cur = (cur as Record<string, unknown>)[parts[i]!]
  }
  return cur
}
