// A friendly request (preset + its fields) into what the API accepts: image, command,
// env, secret env, sidecars. The API validates the result; this only translates.
import { badRequest } from '@sandboxd/core/errors'
import type { CreateSessionInput } from '@sandboxd/api/schema'
import type { PresetRegistry } from './presets/index'
import { isObj, type Catalog } from './presets/catalog'
import { mergeCompose, splitServices, stringMap } from './services'

export interface BuildDeps {
  defaultImage: string | null
  presets: PresetRegistry
  catalog: Catalog
}

export function buildCreate(d: BuildDeps, raw: unknown): CreateSessionInput {
  if (!isObj(raw)) throw badRequest('body must be a JSON object')
  if (typeof raw.owner_id !== 'string' || !raw.owner_id)
    throw badRequest('owner_id is required')
  const preset = d.presets.resolve(raw)
  const x = preset.expand(raw)

  const image =
    (typeof raw.image === 'string' && raw.image.trim()) || x.image || d.defaultImage
  if (!image)
    throw badRequest(
      `preset "${preset.name}" implies no image: send image, or set SANDBOXD_DEFAULT_IMAGE`,
    )

  const out: CreateSessionInput = {
    owner_id: raw.owner_id,
    image,
    env: { ...x.env, ...(raw.env === undefined ? {} : stringMap(raw.env, 'env')) },
    secret_env: {
      ...x.secret_env,
      ...(raw.secret_env === undefined ? {} : stringMap(raw.secret_env, 'secret_env')),
    },
  }
  const cmd = raw.cmd ?? x.cmd
  if (cmd !== undefined) out.cmd = cmd as string[]
  const idle = raw.idle_timeout_s ?? x.idle_timeout_s
  if (idle !== undefined) out.idle_timeout_s = idle as number

  // Sidecars, in start order: the preset's defaults, the caller's picks, the caller's own file.
  const { inline, compose } = splitServices(raw.services, d.catalog)
  if (inline.length > 0) out.services = inline as CreateSessionInput['services']
  const doc = mergeCompose([x.services ?? {}, compose], raw.compose)
  if (doc !== undefined) out.compose = doc as CreateSessionInput['compose']
  return out
}
