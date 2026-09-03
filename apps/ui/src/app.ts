// The UI server: the page, the preset and catalog listings, and POST /sessions, which
// resolves a preset before calling the API. Everything else is forwarded to the API
// with the service token added — the browser never holds it.
import { Hono } from 'hono'
import { hc } from 'hono/client'
import type { ContentfulStatusCode } from 'hono/utils/http-status'
import type { Routes } from '@sandboxd/api/http/routes'
import { HttpError, badRequest } from '@sandboxd/core/errors'
import { logger } from '@sandboxd/core/log'
import type { UiConfig } from './config'
import type { PresetRegistry } from './presets/index'
import type { Catalog } from './presets/catalog'
import { buildCreate } from './sessions'

const log = logger('ui')

export interface AppDeps {
  cfg: Pick<UiConfig, 'apiUrl' | 'serviceToken' | 'defaultImage'>
  presets: PresetRegistry
  catalog: Catalog
  html: () => Promise<string>
  /** Injected by tests. */
  fetch?: typeof fetch
}

export function createApp(d: AppDeps) {
  const call = d.fetch ?? fetch
  const authorization = `Bearer ${d.cfg.serviceToken}`
  const api = hc<Routes>(d.cfg.apiUrl, { headers: { authorization }, fetch: call })

  return new Hono({ strict: false })
    .get('/', async (c) => c.html(await d.html()))
    .get('/presets', (c) => c.json(d.presets.list()))
    .get('/services', (c) => c.json(d.catalog.list()))
    .post('/sessions', async (c) => {
      const raw: unknown = await c.req.json().catch(() => {
        throw badRequest('invalid JSON body')
      })
      const res = await api.sessions.$post({
        json: buildCreate(
          { defaultImage: d.cfg.defaultImage, presets: d.presets, catalog: d.catalog },
          raw,
        ),
      })
      return relay(res)
    })
    .all('/*', async (c) => {
      const url = new URL(c.req.path + new URL(c.req.url).search, d.cfg.apiUrl)
      const headers: Record<string, string> = { authorization }
      const type = c.req.header('content-type')
      if (type) headers['content-type'] = type
      const raw =
        c.req.method === 'GET' || c.req.method === 'HEAD' ? null : await c.req.arrayBuffer()
      const body = raw && raw.byteLength > 0 ? raw : undefined
      return relay(await call(url, { method: c.req.method, headers, body }))
    })
    .onError((err, c) => {
      if (err instanceof HttpError)
        return c.json({ error: err.message }, err.status as ContentfulStatusCode)
      log.error('unhandled', { err: String(err), stack: err.stack })
      return c.json({ error: 'internal error' }, 500)
    })
}

/** The API's answer, status and body untouched. */
const relay = (res: Pick<Response, 'body' | 'status' | 'headers'>) =>
  new Response(res.body, {
    status: res.status,
    headers: { 'content-type': res.headers.get('content-type') ?? 'application/json' },
  })
