const ALPHABET = 'abcdefghijklmnopqrstuvwxyz234567' // base32, DNS-safe
// Human-typable approval code alphabet. Avoids 0/O/1/I.
const CODE = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'

export namespace Id {
  export const random = (bytes = 16): string => {
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

  export const session = () => `s_${random()}`
  export const host = () => `h_${random()}`

  /** Human-typable approval code, e.g. K7QP-3M. */
  export const code = (): string => {
    const pick = (n: number) =>
      Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => CODE[b % CODE.length]).join(
        '',
      )
    return `${pick(4)}-${pick(2)}`
  }
}
