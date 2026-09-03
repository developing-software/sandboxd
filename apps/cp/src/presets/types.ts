// A preset turns a friendly request shape into the generic {image, cmd, env, secret_env, services}
// the core understands. Presets are data (presets/<name>/preset.yaml); this is their runtime shape.
import type { ValidatedServices } from '../services'

/** What a preset implies. image/cmd/idle_timeout_s are defaults the caller may override. */
export interface Expanded {
  env: Record<string, string>
  secret_env: Record<string, string>
  image?: string
  cmd?: string[]
  idle_timeout_s?: number
  /** Sidecars the preset brings by default (catalog references in preset.yaml). */
  services?: ValidatedServices
}

/** The untyped request body. Each preset validates the fields it cares about. */
export type Body = Record<string, unknown>

export type FieldType = 'string' | 'int' | 'enum' | 'url' | 'bool'

/** One request field → one env var, as declared in preset.yaml. Also what GET /presets shows a UI. */
export interface FieldSpec {
  /** Body path; dotted = nested object (`llm.base_url`). */
  name: string
  type: FieldType
  env: string
  required: boolean
  secret: boolean
  default?: string | number | boolean
  values?: string[]
  min?: number
  max?: number
  multiline?: boolean
  description?: string
}

/** Everything about a preset that is not behaviour. Returned by GET /presets. */
export interface PresetInfo {
  name: string
  description: string
  /** null = no preset image; the control plane default applies. */
  image: string | null
  cmd: string[] | null
  idle_timeout_s: number | null
  /** Port a UI should offer to preview; `port_field` names the int field it comes from, when it does. */
  preview: { port: number | null; port_field: string | null } | null
  /** Catalog services the preset brings by default. */
  services: string[]
  fields: FieldSpec[]
}

export interface Preset {
  readonly name: string
  readonly info: PresetInfo
  /** Claim a request that names no `preset` (e.g. coding-agent claims anything with `repo`). */
  claims?(body: Body): boolean
  /** Validate the preset's own fields and expand them. Throws HttpError on bad input. */
  expand(sid: string, body: Body): Expanded
}
