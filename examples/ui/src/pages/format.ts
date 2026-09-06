// How a sandbox reads on a page, shared by the list and the sandbox's own page.
import type { SandboxView } from '@sandboxd/sdk'

export namespace Format {
  /** "queued #2", "running", "ended · idle". */
  export const status = (s: SandboxView) =>
    s.status +
    (s.queue_position ? ` #${s.queue_position}` : '') +
    (s.ended_reason ? ` · ${s.ended_reason}` : '')

  export const tone = (s: SandboxView) =>
    s.status === 'running' ? 'on' : s.status === 'ended' ? 'off' : 'warn'

  /** A unix-millisecond timestamp, or '' for the nulls the API sends before an event. */
  export const when = (ms: number | null) => (ms === null ? '' : new Date(ms).toLocaleString())
}
