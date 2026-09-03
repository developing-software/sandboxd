// HMAC-signed capability tokens. Format: base64url(json).base64url(hmac).
export type TokenKind = 'attach' | 'preview' | 'preview-cookie'
export interface TokenPayload {
  k: TokenKind
  sid: string
  port?: number
  exp: number
}

export class Tokens {
  constructor(private secret: string) {}

  private mac(data: string) {
    return new Bun.CryptoHasher('sha256', this.secret).update(data).digest('base64url')
  }

  sign(p: Omit<TokenPayload, 'exp'>, ttlMs: number): string {
    const body = Buffer.from(JSON.stringify({ ...p, exp: Date.now() + ttlMs })).toString(
      'base64url',
    )
    return `${body}.${this.mac(body)}`
  }

  verify(token: string | null | undefined, kind: TokenKind): TokenPayload | null {
    if (!token) return null
    const [body, sig] = token.split('.')
    if (!body || !sig) return null
    const expected = this.mac(body)
    if (sig.length !== expected.length || !timingSafeEqual(sig, expected)) return null
    try {
      const p = JSON.parse(Buffer.from(body, 'base64url').toString()) as TokenPayload
      if (p.k !== kind || typeof p.sid !== 'string' || p.exp < Date.now()) return null
      return p
    } catch {
      return null
    }
  }
}

function timingSafeEqual(a: string, b: string) {
  let diff = 0
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i)
  return diff === 0
}
