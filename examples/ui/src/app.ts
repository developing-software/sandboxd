// The UI server: the page, the preset listing, and POST /sandboxes, which resolves a
// preset before calling the API. Everything else is forwarded to the API with the
// service token added — the browser never holds it.
import { Hono } from 'hono'
import type { ContentfulStatusCode } from 'hono/utils/http-status'
import { Err } from './errors'
import { Log } from './log'
import type { UiConfig } from './config'
import type { PresetRegistry } from './presets/index'
import { buildCreate } from './sandboxes'

const log = Log.create('ui')

export interface AppDeps {
  cfg: Pick<UiConfig, 'apiUrl' | 'serviceToken' | 'defaultImage'>
  presets: PresetRegistry
  html: () => Promise<string>
  /** Injected by tests. */
  fetch?: typeof fetch
}

export function createApp(d: AppDeps) {
  const call = d.fetch ?? fetch
  const authorization = `Bearer ${d.cfg.serviceToken}`
  // A rejected fetch is transport: the API is down or restarting. Name it, so the page
  // says so instead of "internal error".
  const down = (e: unknown) => {
    log.warn('api unreachable', { url: d.cfg.apiUrl, err: String(e) })
    return new Err.Http(502, `api unreachable at ${d.cfg.apiUrl}`)
  }

  return new Hono({ strict: false })
    .get('/', async (c) => c.html(await d.html()))
    .get('/presets', (c) => c.json(d.presets.list()))
    .post('/sandboxes', async (c) => {
      const raw: unknown = await c.req.json().catch(() => {
        throw Err.badRequest('invalid JSON body')
      })
      const json = buildCreate({ defaultImage: d.cfg.defaultImage, presets: d.presets }, raw)
      const res = await call(new URL('/sandboxes', d.cfg.apiUrl), {
        method: 'POST',
        headers: { authorization, 'content-type': 'application/json' },
        body: JSON.stringify(json),
      }).catch((e) => {
        throw down(e)
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
      const res = await call(url, { method: c.req.method, headers, body }).catch((e) => {
        throw down(e)
      })
      return relay(res)
    })
    .onError((err, c) => {
      if (err instanceof Err.Http)
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
