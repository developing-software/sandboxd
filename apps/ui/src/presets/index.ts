import { Err } from '@sandboxd/core/errors'
import type { Body, Preset, PresetInfo } from './types'

export type { Body, Expanded, FieldSpec, FieldType, Preset, PresetInfo } from './types'
export { loadPresetDir, loadPresetFile, type LoadedPresets } from './loader'
export { parsePresetDoc, type PresetDoc } from './schema'
export { presetFromDoc } from './preset'
export { Catalog } from './catalog'

/** Presets by name, plus the rule for picking one when the request names none:
 *  the first preset that `claims` the body, else `fallback`. */
export class PresetRegistry {
  private byName = new Map<string, Preset>()

  constructor(
    presets: Preset[],
    private fallback = 'custom',
  ) {
    for (const p of presets) {
      if (this.byName.has(p.name)) throw new Error(`preset "${p.name}" is registered twice`)
      this.byName.set(p.name, p)
    }
    if (!this.byName.has(fallback))
      throw new Error(`fallback preset "${fallback}" is not registered`)
  }

  get names(): string[] {
    return [...this.byName.keys()]
  }

  get(name: string): Preset | undefined {
    return this.byName.get(name)
  }

  list(): PresetInfo[] {
    return [...this.byName.values()].map((p) => p.info)
  }

  resolve(body: Body): Preset {
    const name = body.preset
    if (name !== undefined) {
      if (typeof name !== 'string' || !this.byName.has(name))
        throw Err.badRequest(`preset must be one of ${this.names.join(', ')}`)
      return this.byName.get(name)!
    }
    for (const p of this.byName.values()) if (p.claims?.(body)) return p
    return this.byName.get(this.fallback)!
  }
}
