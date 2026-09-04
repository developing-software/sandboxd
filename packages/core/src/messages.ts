// Control messages exchanged as JSON text frames on the host <-> CP tunnel.

export namespace Msg {
  export type EndReason = 'closed' | 'idle' | 'failed' | 'lost' | 'exited'

  /** What the CP asks a host to run. Deliberately generic: the CP knows nothing
   *  about repos, agents or models; presets in the UI turn those into env. */
  export interface Spec {
    sid: string
    image: string
    /** Command exec'd in the PTY. null = the daemon's default entry (SANDBOXD_WORKER_ENTRY). */
    cmd: string[] | null
    idle_timeout_s: number
    /** Non-secret env. Persisted by the CP, visible in the API. */
    env: Record<string, string>
    /** Secret env. Memory-only on both sides; dropped right after docker exec. */
    secret_env: Record<string, string>
  }

  export interface Size {
    cols: number
    rows: number
  }

  // host -> cp
  export type Host =
    | {
        type: 'hello'
        name: string
        fingerprint: string
        running: string[]
        max_sessions: number
        /** Optional; a CP without SANDBOXD_JOIN_TOKEN ignores it. Old workers omit it. */
        join_token?: string
      }
    | { type: 'heartbeat'; running: number; max: number }
    | { type: 'session.started'; sid: string }
    | { type: 'session.ended'; sid: string; reason: EndReason; detail?: string }
    | { type: 'pty.replay'; stream: number } // binary replay frames follow, then live
    | { type: 'pty.closed'; stream: number }
    | { type: 'port.open'; stream: number }
    | { type: 'port.error'; stream: number; msg: string }
    | { type: 'port.close'; stream: number }

  // cp -> host
  export type Cp =
    | { type: 'hello.ok'; host_id: string }
    | { type: 'hello.pending'; host_id: string; code: string }
    | { type: 'hello.rejected'; reason: string }
    | { type: 'session.create'; spec: Spec }
    | { type: 'session.destroy'; sid: string }
    | { type: 'pty.open'; sid: string; stream: number; size: Size }
    | { type: 'pty.resize'; sid: string; size: Size }
    | { type: 'pty.close'; stream: number }
    | { type: 'port.dial'; sid: string; port: number; stream: number }
    | { type: 'port.close'; stream: number }

  /** The browser attach socket. Binary frames are raw PTY bytes; these are the text ones. */
  export namespace Attach {
    export type Client = { type: 'resize'; cols: number; rows: number }
    export type Server = { type: 'closed'; reason: string }
  }

  export const parse = <T>(raw: string): T => {
    const m = JSON.parse(raw)
    if (!m || typeof m.type !== 'string') throw new Error('bad message')
    return m as T
  }
}
