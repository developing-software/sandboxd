const ALPHABET = 'abcdefghijklmnopqrstuvwxyz234567' // base32, DNS-safe

export function randomBase32(bytes = 16): string {
  const buf = crypto.getRandomValues(new Uint8Array(bytes))
  let bits = 0,
    value = 0,
    out = ''
  for (const b of buf) {
    value = (value << 8) | b
    bits += 8
    while (bits >= 5) {
      out += ALPHABET[(value >>> (bits - 5)) & 31]
      bits -= 5
    }
  }
  if (bits > 0) out += ALPHABET[(value << (5 - bits)) & 31]
  return out
}

export const newSessionId = () => `s_${randomBase32()}`
export const newHostId = () => `h_${randomBase32()}`

/** Human-typable approval code, e.g. K7QP-3M. Avoids 0/O/1/I. */
export function newApproveCode(): string {
  const A = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'
  const pick = (n: number) =>
    Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => A[b % A.length]).join('')
  return `${pick(4)}-${pick(2)}`
}
