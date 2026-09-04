// A friendly request (preset + its fields) into what the API accepts: image, command,
// env, secret env. The API validates the result; this only translates.
import { Err } from '@sandboxd/core/errors'
import { Json } from '@sandboxd/core/json'
import type { CreateSessionInput } from '@sandboxd/api/schema'
import type { PresetRegistry } from './presets/index'

export interface BuildDeps {
  defaultImage: string | null
  presets: PresetRegistry
}

export function buildCreate(d: BuildDeps, raw: unknown): CreateSessionInput {
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

  const out: CreateSessionInput = {
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
  return out
}

/** A map that must already be all strings; the API checks names and size. */
function strings(v: unknown, at: string): Record<string, string> {
  if (!Json.isObj(v)) throw Err.badRequest(`${at} must be an object of strings`)
  for (const [k, val] of Object.entries(v))
    if (typeof val !== 'string') throw Err.badRequest(`${at}.${k} must be a string`)
  return v as Record<string, string>
}
