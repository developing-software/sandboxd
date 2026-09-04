// Binary frame: [u32 BE streamId][payload]. Used for PTY bytes and proxied TCP.

export namespace Frame {
  export const HEADER = 4

  export interface Decoded {
    stream: number
    payload: Uint8Array
  }

  export const encode = (stream: number, payload: Uint8Array): Uint8Array => {
    const out = new Uint8Array(HEADER + payload.length)
    new DataView(out.buffer).setUint32(0, stream)
    out.set(payload, HEADER)
    return out
  }

  export const decode = (buf: Uint8Array): Decoded => {
    if (buf.length < HEADER) throw new Error(`frame too short: ${buf.length}`)
    const stream = new DataView(buf.buffer, buf.byteOffset, buf.byteLength).getUint32(0)
    return { stream, payload: buf.subarray(HEADER) }
  }
}
