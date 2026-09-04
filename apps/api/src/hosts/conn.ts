// One connected host: multiplexes PTY and preview-port streams over its single
// WebSocket using the binary framing. Knows nothing about enrollment or the store.
import { Frame } from '@sandboxd/core/framing'
import type { Msg } from '@sandboxd/core/messages'

export interface StreamSub {
  onData(d: Uint8Array): void
  onReplay?(): void
  onOpen?(): void
  onError?(msg: string): void
  onClose(): void
}

export interface PtyHandle {
  write(d: Uint8Array): void
  resize(size: Msg.Size): void
  close(): void
}
export interface PortHandle {
  write(d: Uint8Array): void
  close(): void
}
export interface Capacity {
  running: number
  max: number
}

/** What a HostConn needs from its socket. Bun's ServerWebSocket satisfies it; tests pass a stub. */
export interface Transport {
  send(data: string | Uint8Array): unknown
  close(): void
}

/** Host messages that address one stream rather than the host as a whole. */
export type StreamMsg = Extract<Msg.Host, { stream: number }>

export class HostConn {
  private streams = new Map<number, StreamSub>()
  /** CP-allocated stream ids are odd and never reused within a connection. */
  private nextStream = 1
  running: number
  max: number

  constructor(
    readonly hostId: string,
    readonly transport: Transport,
    running: number,
    max: number,
  ) {
    this.running = running
    this.max = max
  }

  capacity(): Capacity {
    return { running: this.running, max: this.max }
  }
  send(msg: Msg.Cp) {
    this.transport.send(JSON.stringify(msg))
  }
  close() {
    this.transport.close()
  }

  // ---- inbound from the host ----
  onFrame(stream: number, payload: Uint8Array) {
    this.streams.get(stream)?.onData(payload)
  }

  onStreamMsg(msg: StreamMsg) {
    switch (msg.type) {
      case 'pty.replay':
        this.streams.get(msg.stream)?.onReplay?.()
        return
      case 'port.open':
        this.streams.get(msg.stream)?.onOpen?.()
        return
      case 'port.error':
        this.streams.get(msg.stream)?.onError?.(msg.msg)
        this.endStream(msg.stream)
        return
      case 'pty.closed':
      case 'port.close':
        this.endStream(msg.stream)
        return
    }
  }

  /** The socket is gone: every subscriber learns its stream is closed. */
  closeAll() {
    const subs = [...this.streams.values()]
    this.streams.clear()
    for (const s of subs) s.onClose()
  }

  private endStream(stream: number) {
    const s = this.streams.get(stream)
    if (!s) return
    this.streams.delete(stream)
    s.onClose()
  }

  private allocStream(sub: StreamSub): number {
    const stream = this.nextStream
    this.nextStream += 2
    this.streams.set(stream, sub)
    return stream
  }

  // ---- commands to the host ----
  createSession(spec: Msg.Spec) {
    this.send({ type: 'session.create', spec })
    this.running += 1 // optimistic until the next heartbeat
  }

  destroySession(sid: string) {
    this.send({ type: 'session.destroy', sid })
  }

  openPty(sid: string, size: Msg.Size, sub: StreamSub): PtyHandle {
    const stream = this.allocStream(sub)
    this.send({ type: 'pty.open', sid, stream, size })
    return {
      write: (d) => this.transport.send(Frame.encode(stream, d)),
      resize: (sz) => this.send({ type: 'pty.resize', sid, size: sz }),
      close: () => {
        if (this.streams.delete(stream)) this.send({ type: 'pty.close', stream })
      },
    }
  }

  /** Resolves on `port.open`, rejects on `port.error`. `sub.onClose` fires in both the error and the normal-close case. */
  dialPort(
    sid: string,
    port: number,
    sub: Pick<StreamSub, 'onData' | 'onClose'>,
  ): Promise<PortHandle> {
    const { promise, resolve, reject } = Promise.withResolvers<PortHandle>()
    let stream = 0
    const handle: PortHandle = {
      write: (d) => this.transport.send(Frame.encode(stream, d)),
      close: () => {
        if (this.streams.delete(stream)) this.send({ type: 'port.close', stream })
      },
    }
    stream = this.allocStream({
      onData: sub.onData,
      onOpen: () => resolve(handle),
      onError: (m) => reject(new Error(m)),
      onClose: sub.onClose,
    })
    this.send({ type: 'port.dial', sid, port, stream })
    return promise
  }
}
