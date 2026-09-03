// Ring buffer + fan-out for one session's PTY output. The buffer lives here,
// on the host, for the whole session life; the CP stores none of it.
export const DEFAULT_BUFFER = 256 * 1024

export class RingBuffer {
  private chunks: Uint8Array[] = []
  private size = 0
  constructor(private cap = DEFAULT_BUFFER) {}

  push(data: Uint8Array) {
    this.chunks.push(data)
    this.size += data.length
    while (this.size > this.cap && this.chunks.length > 1) {
      const dropped = this.chunks.shift()!
      this.size -= dropped.length
    }
    // A single chunk larger than cap: keep its tail.
    const only = this.chunks[0]
    if (this.chunks.length === 1 && only && only.length > this.cap) {
      this.chunks[0] = only.subarray(only.length - this.cap)
      this.size = this.cap
    }
  }

  snapshot(): Uint8Array {
    const out = new Uint8Array(this.size)
    let off = 0
    for (const c of this.chunks) {
      out.set(c, off)
      off += c.length
    }
    return out
  }
}

export type Subscriber = (data: Uint8Array) => void

export class PtyFanout {
  readonly buffer: RingBuffer
  private subs = new Set<Subscriber>()
  lastActivity = Date.now()

  constructor(cap = DEFAULT_BUFFER) {
    this.buffer = new RingBuffer(cap)
  }

  /** Output from the PTY: buffer it, fan it out, count as activity. */
  emit(data: Uint8Array) {
    this.lastActivity = Date.now()
    this.buffer.push(data)
    for (const s of this.subs) s(data)
  }

  /** Input from a viewer counts as activity too. */
  touch() {
    this.lastActivity = Date.now()
  }

  subscribe(sub: Subscriber): () => void {
    this.subs.add(sub)
    return () => {
      this.subs.delete(sub)
    }
  }

  get viewers() {
    return this.subs.size
  }
}
