// Binary frame: [u32 BE streamId][payload]. Used for PTY bytes and proxied TCP.
export const HEADER = 4;

export function encodeFrame(stream: number, payload: Uint8Array): Uint8Array {
  const out = new Uint8Array(HEADER + payload.length);
  new DataView(out.buffer).setUint32(0, stream);
  out.set(payload, HEADER);
  return out;
}

export function decodeFrame(buf: Uint8Array): { stream: number; payload: Uint8Array } {
  if (buf.length < HEADER) throw new Error(`frame too short: ${buf.length}`);
  const stream = new DataView(buf.buffer, buf.byteOffset, buf.byteLength).getUint32(0);
  return { stream, payload: buf.subarray(HEADER) };
}
