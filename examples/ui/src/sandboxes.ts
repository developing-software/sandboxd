// A friendly request (preset + its fields) into what the API accepts: image, command,
// env, secret env, tags. The API validates the result; this only translates.
import type { CreateSandbox } from '@sandboxd/sdk'
import { Err } from './errors'
import { Json } from './json'
import type { PresetRegistry } from './presets/index'

// CreateSandbox is generated from the control plane's own OpenAPI document, so a field
// added there is a type error here rather than a 400 at runtime.
export type { CreateSandbox }

export interface BuildDeps {
  defaultImage: string | null
  presets: PresetRegistry
}

export function buildCreate(d: BuildDeps, raw: unknown): CreateSandbox {
  if (!Json.isObj(raw)) throw Err.badRequest('body must be a JSON object')
  if (typeof raw.owner_id !== 'string' || !raw.owner_id)
    throw Err.badRequest('owner_id is required')
  const preset = d.presets.resolve(raw)
  const x = preset.expand(raw)

  const image =
    (typeof raw.image === 'string' && raw.image.trim()) || x.image || d.defaultImage
  if (!image)
    throw Err.badRequest(
      `preset "${preset.name}" implies no image: send image, or set SANDBOXD_DEFAULT_IMAGE`,
    )

  const out: CreateSandbox = {
    owner_id: raw.owner_id,
    image,
    env: { ...x.env, ...(raw.env === undefined ? {} : strings(raw.env, 'env')) },
    secret_env: {
      ...x.secret_env,
      ...(raw.secret_env === undefined ? {} : strings(raw.secret_env, 'secret_env')),
    },
  }
  const cmd = raw.cmd ?? x.cmd
  if (cmd !== undefined) out.cmd = cmd as string[]
  const idle = raw.idle_timeout_s ?? x.idle_timeout_s
  if (idle !== undefined) out.idle_timeout_s = idle as number
  // Host tags the caller asks for. The API answers 422 when no approved host carries them.
  if (Array.isArray(raw.tags)) out.tags = raw.tags.map(String)
  return out
}

/** A map that must already be all strings; the API checks names and size. */
function strings(v: unknown, at: string): Record<string, string> {
  if (!Json.isObj(v)) throw Err.badRequest(`${at} must be an object of strings`)
  for (const [k, val] of Object.entries(v))
    if (typeof val !== 'string') throw Err.badRequest(`${at}.${k} must be a string`)
  return v as Record<string, string>
}
