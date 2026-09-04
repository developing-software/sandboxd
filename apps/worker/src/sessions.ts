// Running sessions on this host: one container each, with its PTY, ring buffer and
// idle timer. Everything is torn down together.
import type { Msg } from '@sandboxd/core/messages'
import type { PtyStream, SandboxDriver } from './driver'
import { PtyFanout } from './pty'
import { Log } from '@sandboxd/core/log'

const log = Log.create('worker.sessions')

interface Running {
  spec: Msg.Spec
  containerId: string
  pty: PtyStream
  fanout: PtyFanout
  size: Msg.Size
  ending: boolean
}

export interface SessionEvents {
  started(sid: string): void
  ended(sid: string, reason: Msg.EndReason, detail?: string): void
}

const NO_EVENTS: SessionEvents = { started() {}, ended() {} }
const INITIAL_SIZE: Msg.Size = { cols: 120, rows: 40 }

export class SessionManager {
  private sessions = new Map<string, Running>()
  private timer: ReturnType<typeof setInterval>
  private events: SessionEvents = NO_EVENTS

  constructor(
    private driver: SandboxDriver,
    private entry: string[],
  ) {
    this.timer = setInterval(() => this.reapIdle(), 15_000)
  }

  /** Subscribe after construction; the tunnel is built after the manager and needs it too. */
  on(events: SessionEvents) {
    this.events = events
  }

  running(): string[] {
    return [...this.sessions.keys()]
  }
  count() {
    return this.sessions.size
  }
  has(sid: string) {
    return this.sessions.has(sid)
  }

  async create(spec: Msg.Spec) {
    if (this.sessions.has(spec.sid)) return
    const sid = spec.sid
    let containerId: string | undefined
    try {
      containerId = await this.driver.create({ sid, image: spec.image })
      const env = {
        ...spec.env,
        ...spec.secret_env,
        TERM: 'xterm-256color',
        SANDBOXD_SESSION_ID: sid,
      }
      const size = { ...INITIAL_SIZE }
      const pty = await this.driver.attach(containerId, spec.cmd ?? this.entry, env, size)
      const fanout = new PtyFanout()
      this.sessions.set(sid, { spec, containerId, pty, fanout, size, ending: false })
      pty.onData((d) => fanout.emit(d))
      pty.onExit(() => {
        void this.end(sid, 'exited')
      })
      // Secrets were handed to docker; drop our copy.
      spec.secret_env = {}
      this.events.started(sid)
      log.info('session started', { sid, container: containerId.slice(0, 12) })
    } catch (e) {
      log.error('session create failed', { sid, err: String(e) })
      if (containerId) await this.remove(sid, containerId)
      this.events.ended(sid, 'failed', String(e))
    }
  }

  /** Best effort: a container that is already gone is not an error worth failing on. */
  private async remove(sid: string, id: string) {
    await this.driver
      .destroy(id)
      .catch((e) => log.warn('destroy failed', { sid, id: id.slice(0, 12), err: String(e) }))
  }

  attachViewer(
    sid: string,
    size: Msg.Size,
    sub: (d: Uint8Array) => void,
  ): { replay: Uint8Array; unsubscribe: () => void } | null {
    const run = this.sessions.get(sid)
    if (!run) return null
    const unsubscribe = run.fanout.subscribe(sub)
    this.resize(sid, size)
    return { replay: run.fanout.buffer.snapshot(), unsubscribe }
  }

  write(sid: string, data: Uint8Array) {
    const run = this.sessions.get(sid)
    if (!run) return
    run.fanout.touch()
    run.pty.write(data)
  }

  resize(sid: string, size: Msg.Size) {
    const run = this.sessions.get(sid)
    if (!run || (run.size.cols === size.cols && run.size.rows === size.rows)) return
    run.size = size // last resize wins
    void run.pty.resize(size)
  }

  containerOf(sid: string): string | undefined {
    return this.sessions.get(sid)?.containerId
  }

  async end(sid: string, reason: Msg.EndReason, detail?: string) {
    const run = this.sessions.get(sid)
    if (!run || run.ending) return
    run.ending = true
    this.sessions.delete(sid)
    try {
      run.pty.close()
    } catch {}
    await this.remove(sid, run.containerId)
    log.info('session ended', { sid, reason })
    this.events.ended(sid, reason, detail)
  }

  private reapIdle() {
    const now = Date.now()
    for (const [sid, run] of this.sessions) {
      if (now - run.fanout.lastActivity > run.spec.idle_timeout_s * 1000)
        void this.end(sid, 'idle')
    }
  }

  async endAll(reason: Msg.EndReason) {
    clearInterval(this.timer)
    await Promise.all(this.running().map((sid) => this.end(sid, reason)))
  }
}
