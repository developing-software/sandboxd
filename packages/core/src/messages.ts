// Control messages exchanged as JSON text frames on the host <-> CP tunnel.

export type EndReason = 'closed' | 'idle' | 'failed' | 'lost' | 'exited'

/** A sidecar container on the session's private network, reachable from the
 *  sandbox as `name`. Started before the sandbox, removed with it. */
export interface ServiceSpec {
  name: string
  image: string
  /** Non-secret env, set at container create. Persisted by the CP. */
  env: Record<string, string>
  /** Secret env, set at container create. Memory-only on the CP; visible in `docker inspect` on the host. */
  secret_env: Record<string, string>
  /** Command override (compose `command:`); null = the image's own. */
  cmd: string[] | null
  /** Block the sandbox until TCP `port` on the service accepts a connection, or fail the session after `timeout_s`. */
  ready: { port: number; timeout_s: number } | null
}

/** What the CP asks a host to run. Deliberately generic: the CP knows nothing
 *  about repos, agents or models; presets on the CP side turn those into env. */
export interface SessionSpec {
  sid: string
  image: string
  /** Command exec'd in the PTY. null = the daemon's default entry (SANDBOXD_WORKER_ENTRY). */
  cmd: string[] | null
  idle_timeout_s: number
  /** Non-secret env. Persisted by the CP, visible in the API. */
  env: Record<string, string>
  /** Secret env. Memory-only on both sides; dropped right after docker exec. */
  secret_env: Record<string, string>
  /** Sidecars. Empty = the sandbox alone on the default bridge network. */
  services: ServiceSpec[]
}

export interface Size {
  cols: number
  rows: number
}

// host -> cp
export type HostMsg =
  | {
      type: 'hello'
      name: string
      fingerprint: string
      running: string[]
      max_sessions: number
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
export type CpMsg =
  | { type: 'hello.ok'; host_id: string }
  | { type: 'hello.pending'; host_id: string; code: string }
  | { type: 'hello.rejected'; reason: string }
  | { type: 'session.create'; spec: SessionSpec }
  | { type: 'session.destroy'; sid: string }
  | { type: 'pty.open'; sid: string; stream: number; size: Size }
  | { type: 'pty.resize'; sid: string; size: Size }
  | { type: 'pty.close'; stream: number }
  | { type: 'port.dial'; sid: string; port: number; stream: number }
  | { type: 'port.close'; stream: number }

// browser -> cp on the attach socket (binary frames are raw PTY bytes)
export type AttachClientMsg = { type: 'resize'; cols: number; rows: number }
// cp -> browser
export type AttachServerMsg = { type: 'closed'; reason: string }

export function parseMsg<T>(raw: string): T {
  const m = JSON.parse(raw)
  if (!m || typeof m.type !== 'string') throw new Error('bad message')
  return m as T
}
