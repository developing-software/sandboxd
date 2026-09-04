// Outbound WebSocket to the CP. Reconnects with backoff. Multiplexes PTY and
// preview-port streams over one socket using the binary framing.
import { encodeFrame, decodeFrame } from '@sandboxd/core/framing'
import { parseMsg, type CpMsg, type HostMsg } from '@sandboxd/core/messages'
import type { WorkerConfig } from './config'
import type { SandboxDriver, Duplex } from './driver'
import type { SessionManager } from './sessions'
import { logger } from '@sandboxd/core/log'

const log = logger('worker.tunnel')

type Stream =
  | { kind: 'pty'; sid: string; unsubscribe: () => void }
  | { kind: 'port'; sock: Duplex }

export class Tunnel {
  private ws: WebSocket | null = null
  private streams = new Map<number, Stream>()
  private backoff = 1000
  private heartbeat: ReturnType<typeof setInterval> | null = null
  private stopped = false
  hostId: string | null = null

  constructor(
    private cfg: WorkerConfig,
    private sessions: SessionManager,
    private driver: SandboxDriver,
  ) {}

  start() {
    this.connect()
  }

  stop() {
    this.stopped = true
    this.ws?.close()
  }

  send(msg: HostMsg) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(msg))
  }

  /** Close every viewer stream for a session (called before session.ended). */
  closePtyStreams(sid: string) {
    for (const [stream, s] of this.streams) {
      if (s.kind === 'pty' && s.sid === sid) {
        s.unsubscribe()
        this.streams.delete(stream)
        this.send({ type: 'pty.closed', stream })
      }
    }
  }

  private sendBinary(stream: number, data: Uint8Array) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(encodeFrame(stream, data))
  }

  private connect() {
    if (this.stopped) return
    const url = `${this.cfg.cpUrl}/tunnel`
    log.info('connecting', { url })
    const ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'
    this.ws = ws

    ws.onopen = () => {
      this.backoff = 1000
      this.send({
        type: 'hello',
        name: this.cfg.name,
        fingerprint: this.cfg.fingerprint,
        running: this.sessions.running(),
        max_sessions: this.cfg.maxSessions,
        ...(this.cfg.joinToken ? { join_token: this.cfg.joinToken } : {}),
      })
    }
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') this.onControl(parseMsg<CpMsg>(ev.data))
      else this.onBinary(new Uint8Array(ev.data as ArrayBuffer))
    }
    ws.onclose = () => {
      log.warn('disconnected')
      this.teardownStreams()
      if (this.heartbeat) clearInterval(this.heartbeat)
      this.heartbeat = null
      if (this.stopped) return
      setTimeout(() => this.connect(), this.backoff)
      this.backoff = Math.min(this.backoff * 2, 30_000)
    }
    ws.onerror = () => {
      /* onclose follows */
    }
  }

  private teardownStreams() {
    for (const s of this.streams.values()) {
      if (s.kind === 'pty') s.unsubscribe()
      else s.sock.end()
    }
    this.streams.clear()
  }

  private startHeartbeat() {
    if (this.heartbeat) return
    this.heartbeat = setInterval(() => {
      this.send({
        type: 'heartbeat',
        running: this.sessions.count(),
        max: this.cfg.maxSessions,
      })
    }, 10_000)
  }

  private onControl(msg: CpMsg) {
    switch (msg.type) {
      case 'hello.ok':
        this.hostId = msg.host_id
        log.info('connected', { host_id: msg.host_id })
        this.startHeartbeat()
        return
      case 'hello.pending':
        this.hostId = msg.host_id
        console.log(
          `\n  This host is waiting for approval on the control plane.\n  Host id: ${msg.host_id}\n  Code:    ${msg.code}\n  (or set SANDBOXD_JOIN_TOKEN here and on the control plane to skip this)\n`,
        )
        return
      case 'hello.rejected':
        log.error('rejected by control plane', { reason: msg.reason })
        return
      case 'session.create':
        void this.sessions.create(msg.spec)
        return
      case 'session.destroy':
        void this.sessions.end(msg.sid, 'closed')
        return
      case 'pty.open': {
        const stream = msg.stream
        const attached = this.sessions.attachViewer(msg.sid, msg.size, (d) =>
          this.sendBinary(stream, d),
        )
        if (!attached) {
          this.send({ type: 'pty.closed', stream })
          return
        }
        this.streams.set(stream, {
          kind: 'pty',
          sid: msg.sid,
          unsubscribe: attached.unsubscribe,
        })
        this.send({ type: 'pty.replay', stream })
        if (attached.replay.length) this.sendBinary(stream, attached.replay)
        return
      }
      case 'pty.resize':
        this.sessions.resize(msg.sid, msg.size)
        return
      case 'pty.close': {
        const s = this.streams.get(msg.stream)
        if (s?.kind === 'pty') s.unsubscribe()
        this.streams.delete(msg.stream)
        return
      }
      case 'port.dial':
        void this.dial(msg.sid, msg.port, msg.stream)
        return
      case 'port.close': {
        const s = this.streams.get(msg.stream)
        if (s?.kind === 'port') s.sock.end()
        this.streams.delete(msg.stream)
        return
      }
    }
  }

  private async dial(sid: string, port: number, stream: number) {
    const containerId = this.sessions.containerOf(sid)
    if (!containerId) {
      this.send({ type: 'port.error', stream, msg: 'no such session' })
      return
    }
    try {
      const sock = await this.driver.dial(containerId, port)
      this.streams.set(stream, { kind: 'port', sock })
      sock.onData((d) => this.sendBinary(stream, d))
      sock.onClose(() => {
        if (this.streams.delete(stream)) this.send({ type: 'port.close', stream })
      })
      this.send({ type: 'port.open', stream })
    } catch (e) {
      this.send({ type: 'port.error', stream, msg: String(e) })
    }
  }

  private onBinary(buf: Uint8Array) {
    const { stream, payload } = decodeFrame(buf)
    const s = this.streams.get(stream)
    if (!s) return
    if (s.kind === 'pty') this.sessions.write(s.sid, payload)
    else s.sock.write(payload)
  }
}
