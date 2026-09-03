// Preview proxy entry: https://<port>-<sid>.<previewDomain>/... -> tunnel stream ->
// daemon -> container:port. Handles the token/cookie gate and routes to the
// HTTP forwarder or the WebSocket bridge.
import type { PortDialer } from '../hosts/hub'
import type { Store } from '../store'
import { Tokens } from '../tokens'
import { COOKIE, proxyOnce, type Dial } from './http'
import { proxyWebSocket, type PreviewUpgrader } from './ws-bridge'

export { COOKIE } from './http'
export type { PreviewWsData, PreviewUpgrader } from './ws-bridge'

const COOKIE_TTL_MS = 12 * 60 * 60 * 1000

export interface PreviewTarget {
  port: number
  sid: string
}

export class PreviewProxy {
  private hostRe: RegExp

  constructor(
    private store: Store,
    private hub: PortDialer,
    private tokens: Tokens,
    private previewDomain: string,
  ) {
    this.hostRe = new RegExp(
      `^(\\d{1,5})-(s_[a-z2-7]+)\\.${previewDomain.replace(/\./g, '\\.')}(?::\\d+)?$`,
      'i',
    )
  }

  /** Returns null when the Host header is not a preview subdomain. */
  match(req: Request): PreviewTarget | null {
    const m = this.hostRe.exec(req.headers.get('host') ?? '')
    return m ? { port: Number(m[1]), sid: m[2]!.toLowerCase() } : null
  }

  /** Returns undefined when the request was upgraded to a WebSocket. */
  async handle(
    req: Request,
    { port, sid }: PreviewTarget,
    server: PreviewUpgrader,
  ): Promise<Response | undefined> {
    const url = new URL(req.url)
    const secure =
      url.protocol === 'https:' || req.headers.get('x-forwarded-proto') === 'https'

    // First visit: ?t=<preview token> -> set cookie -> redirect without it.
    const t = url.searchParams.get('t')
    if (t) {
      const p = this.tokens.verify(t, 'preview')
      if (!p || p.sid !== sid || p.port !== port)
        return new Response('invalid or expired preview token', { status: 401 })
      url.searchParams.delete('t')
      const cookie = this.tokens.sign({ k: 'preview-cookie', sid, port }, COOKIE_TTL_MS)
      return new Response(null, {
        status: 302,
        headers: {
          location: url.pathname + url.search,
          'set-cookie': `${COOKIE}=${cookie}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${COOKIE_TTL_MS / 1000}${secure ? '; Secure' : ''}`,
        },
      })
    }

    const cookies = parseCookies(req.headers.get('cookie'))
    const c = this.tokens.verify(cookies[COOKIE], 'preview-cookie')
    if (!c || c.sid !== sid || c.port !== port)
      return new Response('preview requires a token: open the link from the app', {
        status: 401,
      })

    const s = this.store.session(sid)
    if (!s || s.status !== 'running' || !s.host_id)
      return new Response('session is not running', { status: 502 })
    if (!this.hub.isOnline(s.host_id)) return new Response('host is offline', { status: 502 })
    const dial: Dial = (sub) => this.hub.dialPort(s.host_id!, sid, port, sub)

    if (req.headers.get('upgrade')?.toLowerCase() === 'websocket')
      return proxyWebSocket(req, port, dial, server)
    return proxyOnce(req, port, dial)
  }
}

function parseCookies(h: string | null): Record<string, string> {
  const out: Record<string, string> = {}
  for (const part of (h ?? '').split(';')) {
    const i = part.indexOf('=')
    if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim()
  }
  return out
}
