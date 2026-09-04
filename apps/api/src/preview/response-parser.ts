// Incremental HTTP/1.1 response parser: head, then body (chunked, length-delimited, or until close).
import { Bytes } from '@sandboxd/core/bytes'

export type ParseEvent =
  | { kind: 'head'; status: number; headers: Headers }
  | { kind: 'body'; data: Uint8Array }
  | { kind: 'end' }

export class ResponseParser {
  headDone = false
  private buf: Uint8Array = new Uint8Array(0)
  private mode: 'chunked' | 'length' | 'close' = 'close'
  private remaining = 0 // for length mode, or current chunk in chunked mode
  private chunkState: 'size' | 'data' | 'crlf' | 'done' = 'size'

  feed(data: Uint8Array): ParseEvent[] {
    this.buf = this.buf.length ? Bytes.concat([this.buf, data]) : data
    const events: ParseEvent[] = []

    if (!this.headDone) {
      const idx = Bytes.crlf2(this.buf)
      if (idx < 0) return events
      const head = new TextDecoder().decode(this.buf.subarray(0, idx))
      this.buf = this.buf.subarray(idx + 4)
      const [statusLine, ...rest] = head.split('\r\n')
      const status = Number(statusLine?.split(' ')[1] ?? 502)
      const headers = new Headers()
      for (const l of rest) {
        const i = l.indexOf(':')
        if (i > 0) headers.append(l.slice(0, i).trim(), l.slice(i + 1).trim())
      }
      this.headDone = true
      const te = headers.get('transfer-encoding')?.toLowerCase()
      const cl = headers.get('content-length')
      if (te?.includes('chunked')) this.mode = 'chunked'
      else if (cl !== null) {
        this.mode = 'length'
        this.remaining = Number(cl)
      }
      events.push({ kind: 'head', status, headers })
      if (this.mode === 'length' && this.remaining === 0) {
        events.push({ kind: 'end' })
        return events
      }
    }

    if (this.mode === 'close') {
      if (this.buf.length) {
        events.push({ kind: 'body', data: this.buf })
        this.buf = new Uint8Array(0)
      }
      return events
    }
    if (this.mode === 'length') {
      const take = Math.min(this.remaining, this.buf.length)
      if (take) {
        events.push({ kind: 'body', data: this.buf.subarray(0, take) })
        this.buf = this.buf.subarray(take)
        this.remaining -= take
      }
      if (this.remaining === 0) events.push({ kind: 'end' })
      return events
    }
    // chunked
    while (true) {
      if (this.chunkState === 'size') {
        const i = Bytes.crlf(this.buf)
        if (i < 0) break
        const size = parseInt(
          new TextDecoder().decode(this.buf.subarray(0, i)).split(';')[0]!.trim(),
          16,
        )
        this.buf = this.buf.subarray(i + 2)
        if (size === 0) {
          this.chunkState = 'done'
          events.push({ kind: 'end' })
          break
        }
        this.remaining = size
        this.chunkState = 'data'
      } else if (this.chunkState === 'data') {
        const take = Math.min(this.remaining, this.buf.length)
        if (take) {
          events.push({ kind: 'body', data: this.buf.subarray(0, take) })
          this.buf = this.buf.subarray(take)
          this.remaining -= take
        }
        if (this.remaining > 0) break
        this.chunkState = 'crlf'
      } else if (this.chunkState === 'crlf') {
        if (this.buf.length < 2) break
        this.buf = this.buf.subarray(2)
        this.chunkState = 'size'
      } else break
    }
    return events
  }
}
