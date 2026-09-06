// Which port of a sandbox is worth offering to open. A preset that declares a preview port
// declares it as a field, and a field is an env var, so the sandbox the API hands back
// still names the port long after the create form's `?port=` is gone — which is what lets
// the list page link one it did not create.
import type { SandboxView } from '@sandboxd/sdk'
import type { PresetInfo } from './presets/types'

/** A port worth offering, and the preset that declared it — the label for the link. */
interface Previewable {
  port: number
  preset: string
}

export namespace Preview {
  /**
   * The port a sandbox's own env implies, or null when nothing declared one. Only `env` is
   * read: a caller who set the same variable on a stock image meant the same thing, and a
   * link to a port nothing answers on is a 502, not a wrong answer.
   */
  export const detect = (
    presets: PresetInfo[],
    s: Pick<SandboxView, 'env'>,
  ): Previewable | null => {
    for (const p of presets) {
      const name = portEnv(p)
      if (name === undefined) continue
      const port = Number(s.env[name])
      if (Number.isInteger(port) && port > 0 && port < 65536) return { port, preset: p.name }
    }
    return null
  }
}

/** The env var a preset's preview port arrives in. A literal `preview: {port}` names no
 *  field, so it leaves no trace on the sandbox and cannot be recovered from one. */
const portEnv = (p: PresetInfo) =>
  p.preview?.port_field
    ? p.fields.find((f) => f.name === p.preview?.port_field)?.env
    : undefined
