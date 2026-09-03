// Parent-app-facing HTTP API: a thin route table over the host and session
// services. Auth = service token, except for the few public routes below.
import type { CpConfig } from './config'
import type { Tokens } from './tokens'
import type { HostService } from './hosts/service'
import type { SessionService } from './sessions'
import type { PresetInfo, CatalogView } from './presets/index'
import type { AttachData } from './attach'
import { HttpError, notFound, unauthorized } from './errors'
import { Router, makeCtx, type Ctx } from './router'
import { logger } from '@sandboxd/core/log'

const log = logger('api')

type Upgrader = { upgrade(req: Request, opts: { data: AttachData }): boolean }

export class Api {
  private router = new Router()

  constructor(
    private cfg: Pick<CpConfig, 'serviceToken' | 'dev'>,
    private tokens: Tokens,
    private hosts: HostService,
    private sessions: SessionService,
    private presets: { list(): PresetInfo[] },
    private catalog: { list(): CatalogView[] },
  ) {
    const r = this.router
    r.public('GET', '/healthz', () => json({ ok: true }))
    if (cfg.dev) r.public('GET', '/dev', () => devPage())

    // What a caller can ask for: preset field schemas and the service catalog. Both are data on disk.
    r.add('GET', '/presets', () => json(this.presets.list()))
    r.add('GET', '/services', () => json(this.catalog.list()))

    r.add('GET', '/hosts', () => json(this.hosts.list()))
    r.add('POST', '/hosts/:id/approve', async (c) => {
      const b = await c.json<{ code?: string }>()
      this.hosts.approve(c.params.id!, b.code)
      return json({ ok: true })
    })
    r.add('POST', '/hosts/:id/revoke', (c) => {
      this.hosts.revoke(c.params.id!)
      return json({ ok: true })
    })

    r.add('POST', '/sessions', async (c) => json(this.sessions.create(await c.json()), 201))
    r.add('GET', '/sessions', (c) =>
      json(this.sessions.list(c.url.searchParams.get('owner_id') ?? undefined)),
    )
    r.add('GET', '/sessions/:id', (c) => json(this.sessions.get(c.params.id!, ownerOf(c))))
    r.add('DELETE', '/sessions/:id', (c) =>
      json(this.sessions.cancel(c.params.id!, ownerOf(c))),
    )
    r.add('POST', '/sessions/:id/attach-token', async (c) => {
      const b = await c.json<{ owner_id?: string }>()
      return json(this.sessions.attachToken(c.params.id!, ownerOf(c, b)))
    })
    r.add('POST', '/sessions/:id/preview-token', async (c) => {
      const b = await c.json<{ owner_id?: string; port?: number }>()
      return json(this.sessions.previewToken(c.params.id!, ownerOf(c, b), b.port))
    })
  }

  async handle(req: Request, server: Upgrader): Promise<Response> {
    try {
      return await this.route(req, server)
    } catch (e) {
      if (e instanceof HttpError) return json({ error: e.message }, e.status)
      log.error('unhandled', { err: String(e), stack: (e as Error)?.stack })
      return json({ error: 'internal error' }, 500)
    }
  }

  private async route(req: Request, server: Upgrader): Promise<Response> {
    const url = new URL(req.url)
    const path = url.pathname.replace(/\/+$/, '') || '/'

    // The attach socket is its own thing: token-gated, and it has to upgrade.
    if (path === '/attach' && req.method === 'GET') return this.attach(req, url, server)

    const m = this.router.match(req.method, path)
    if (!m) throw notFound('route')
    if (m.auth && req.headers.get('authorization') !== `Bearer ${this.cfg.serviceToken}`)
      throw unauthorized()
    return m.handler(makeCtx(req, url, m.params))
  }

  private attach(req: Request, url: URL, server: Upgrader): Response {
    const p = this.tokens.verify(url.searchParams.get('token'), 'attach')
    if (!p) return json({ error: 'invalid or expired attach token' }, 401)
    const data: AttachData = {
      kind: 'attach',
      sid: p.sid,
      pty: null,
      cols: Number(url.searchParams.get('cols') ?? 120) || 120,
      rows: Number(url.searchParams.get('rows') ?? 40) || 40,
    }
    return server.upgrade(req, { data })
      ? new Response(null, { status: 101 })
      : json({ error: 'websocket upgrade required' }, 426)
  }
}

/** owner_id from the body when there is one, else the query string. */
const ownerOf = (c: Ctx, body?: { owner_id?: string }) =>
  body?.owner_id ?? c.url.searchParams.get('owner_id')

async function devPage() {
  // Re-read on every request so edits show up on reload without a restart.
  const html = await Bun.file(new URL('./dev/index.html', import.meta.url)).text()
  return new Response(html, { headers: { 'content-type': 'text/html; charset=utf-8' } })
}

const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), {
    status,
    headers: { 'content-type': 'application/json' },
  })
