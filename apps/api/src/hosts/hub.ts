// Host hub: the registry of connected daemons. Owns the tunnel sockets, runs
// enrollment on `hello`, and hands stream traffic to the right HostConn.
// Consumers depend on the narrow interfaces below, not on the class.
import type { ServerWebSocket } from 'bun'
import { Frame } from '@sandboxd/core/framing'
import { Msg } from '@sandboxd/core/messages'
import type { Store } from '../store'
import { Log } from '@sandboxd/core/log'
import {
  HostConn,
  type Capacity,
  type PortHandle,
  type PtyHandle,
  type StreamSub,
} from './conn'
import { enroll } from './enrollment'

const log = Log.create('hub')

export interface TunnelData {
  kind: 'tunnel'
  hostId: string | null
}
type WS = ServerWebSocket<TunnelData>

export interface HubEvents {
  hostOnline(hostId: string, running: string[]): void
  heartbeat(hostId: string): void
  sessionStarted(sid: string): void
  sessionEnded(sid: string, reason: Msg.EndReason, detail?: string): void
}

/** What the scheduler needs from the hub. */
export interface HostPlacement {
  isOnline(hostId: string): boolean
  capacity(hostId: string): Capacity | null
  createSession(hostId: string, spec: Msg.Spec): boolean
  destroySession(hostId: string, sid: string): void
}

/** What the preview proxy needs from the hub. */
export interface PortDialer {
  isOnline(hostId: string): boolean
  dialPort(
    hostId: string,
    sid: string,
    port: number,
    sub: Pick<StreamSub, 'onData' | 'onClose'>,
  ): Promise<PortHandle>
}

/** What the attach bridge needs from the hub. */
export interface PtyOpener {
  openPty(hostId: string, sid: string, size: Msg.Size, sub: StreamSub): PtyHandle | null
}

/** What the host admin API needs from the hub. */
export interface HostPresence {
  isOnline(hostId: string): boolean
  capacity(hostId: string): Capacity | null
  notifyApproved(hostId: string): void
}

const NO_EVENTS: HubEvents = {
  hostOnline() {},
  heartbeat() {},
  sessionStarted() {},
  sessionEnded() {},
}

export class HostHub implements HostPlacement, PortDialer, PtyOpener, HostPresence {
  private conns = new Map<string, HostConn>()
  /** Sockets that said hello and are waiting for an admin to approve them. */
  private pending = new Map<string, WS>()
  private events: HubEvents = NO_EVENTS

  constructor(
    private store: Store,
    private joinToken: string | null = null,
  ) {}

  /** Subscribe after construction, so the hub and its subscriber need no two-way constructor wiring. */
  on(events: HubEvents) {
    this.events = events
  }

  isOnline(hostId: string) {
    return this.conns.has(hostId)
  }
  capacity(hostId: string): Capacity | null {
    return this.conns.get(hostId)?.capacity() ?? null
  }

  // ---- socket lifecycle (called from Bun.serve) ----
  onOpen(ws: WS) {
    log.info('worker connected', { from: ws.remoteAddress })
  }

  onMessage(ws: WS, raw: string | Buffer) {
    if (typeof raw === 'string') {
      const msg = Msg.parse<Msg.Host>(raw)
      if (msg.type === 'hello') return this.onHello(ws, msg)
      const c = this.connOf(ws)
      if (c) this.onControl(c, msg) // else: not approved / not hello'd yet
      return
    }
    const c = this.connOf(ws)
    if (!c) return
    const { stream, payload } = Frame.decode(new Uint8Array(raw))
    c.onFrame(stream, payload)
  }

  onClose(ws: WS) {
    const hostId = ws.data.hostId
    if (!hostId) {
      log.info('worker disconnected before hello')
      return
    }
    const c = this.conns.get(hostId)
    if (c && c.transport === ws) {
      this.conns.delete(hostId)
      c.closeAll()
      log.warn('host offline', { hostId })
    }
    this.pending.delete(hostId)
  }

  private connOf(ws: WS): HostConn | undefined {
    return ws.data.hostId ? this.conns.get(ws.data.hostId) : undefined
  }

  // ---- enrollment ----
  private onHello(ws: WS, hello: Extract<Msg.Host, { type: 'hello' }>) {
    const e = enroll(this.store, hello, this.joinToken)
    const { id: hostId, name } = e.host
    ws.data.hostId = hostId
    switch (e.kind) {
      case 'rejected':
        log.warn('revoked host tried to connect', { hostId, name })
        ws.send(JSON.stringify({ type: 'hello.rejected', reason: e.reason } satisfies Msg.Cp))
        ws.close()
        return
      case 'pending':
        if (e.badToken)
          log.warn('join token did not match; waiting for the code', { hostId, name })
        log.info(e.isNew ? 'new host pending approval' : 'host waiting for approval', {
          hostId,
          name,
          code: e.code,
        })
        this.pending.set(hostId, ws)
        ws.send(
          JSON.stringify({
            type: 'hello.pending',
            host_id: hostId,
            code: e.code,
          } satisfies Msg.Cp),
        )
        return
      case 'accepted':
        if (e.joined) log.info('host approved by join token', { hostId, name })
        this.accept(ws, hostId, hello.running, hello.max_sessions)
    }
  }

  private accept(ws: WS, hostId: string, running: string[], max: number) {
    const old = this.conns.get(hostId)
    if (old && old.transport !== ws) {
      old.closeAll()
      old.close()
    }
    const c = new HostConn(hostId, ws, running.length, max)
    this.conns.set(hostId, c)
    this.pending.delete(hostId)
    c.send({ type: 'hello.ok', host_id: hostId })
    log.info('host online', { hostId, running: running.length, max })
    this.events.hostOnline(hostId, running)
  }

  /** Called after an admin approves: promote a waiting connection without a reconnect. */
  notifyApproved(hostId: string) {
    const ws = this.pending.get(hostId)
    if (!ws) return
    const host = this.store.hostById(hostId)
    this.accept(ws, hostId, [], host?.max_sessions ?? 0)
  }

  // ---- control from an accepted host ----
  private onControl(c: HostConn, msg: Msg.Host) {
    switch (msg.type) {
      case 'heartbeat':
        c.running = msg.running
        c.max = msg.max
        this.store.touchHost(c.hostId, { max_sessions: msg.max })
        this.events.heartbeat(c.hostId)
        return
      case 'session.started':
        this.events.sessionStarted(msg.sid)
        return
      case 'session.ended':
        this.events.sessionEnded(msg.sid, msg.reason, msg.detail)
        return
      case 'hello':
        return // handled in onMessage
      default:
        c.onStreamMsg(msg)
    }
  }

  // ---- commands to hosts ----
  createSession(hostId: string, spec: Msg.Spec): boolean {
    const c = this.conns.get(hostId)
    if (!c) return false
    c.createSession(spec)
    return true
  }

  destroySession(hostId: string, sid: string) {
    this.conns.get(hostId)?.destroySession(sid)
  }

  openPty(hostId: string, sid: string, size: Msg.Size, sub: StreamSub): PtyHandle | null {
    return this.conns.get(hostId)?.openPty(sid, size, sub) ?? null
  }

  dialPort(
    hostId: string,
    sid: string,
    port: number,
    sub: Pick<StreamSub, 'onData' | 'onClose'>,
  ): Promise<PortHandle> {
    const c = this.conns.get(hostId)
    if (!c) return Promise.reject(new Error('host offline'))
    return c.dialPort(sid, port, sub)
  }
}
