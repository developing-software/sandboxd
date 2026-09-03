// Small byte helpers shared by the framing, proxy and docker code.

export function concat(parts: Uint8Array[]): Uint8Array {
  if (parts.length === 1) return parts[0]!
  let size = 0
  for (const p of parts) size += p.length
  const out = new Uint8Array(size)
  let o = 0
  for (const p of parts) {
    out.set(p, o)
    o += p.length
  }
  return out
}

/** Index of the first "\r\n", or -1. */
export function findCRLF(b: Uint8Array): number {
  for (let i = 0; i + 1 < b.length; i++) if (b[i] === 13 && b[i + 1] === 10) return i
  return -1
}

/** Index of the first "\r\n\r\n" (end of an HTTP head), or -1. */
export function findCRLF2(b: Uint8Array): number {
  for (let i = 0; i + 3 < b.length; i++) {
    if (b[i] === 13 && b[i + 1] === 10 && b[i + 2] === 13 && b[i + 3] === 10) return i
  }
  return -1
}
