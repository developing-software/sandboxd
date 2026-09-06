// The JSON half of the UI, under /api. GET /api/presets and POST /api/sandboxes are this
// app's own: the latter resolves a preset, then calls the control plane with the SDK.
// Everything else is forwarded byte for byte with the service token added, which is why
// the browser talks to this app and never to the API directly.
import { createSandbox, createSandboxd } from '@sandboxd/sdk'
import { Hono } from 'hono'
import type { ContentfulStatusCode } from 'hono/utils/http-status'
import type { UiConfig } from './config'
import { Err } from './errors'
import { Log } from './log'
import type { PresetRegistry } from './presets/index'
import { buildCreate } from './sandboxes'

const log = Log.create('ui')

export interface ApiDeps {
  cfg: Pick<UiConfig, 'apiUrl' | 'serviceToken' | 'defaultImage'>
  presets: PresetRegistry
  /** Injected by tests. */
  fetch?: typeof fetch
}

export function createApi(d: ApiDeps) {
  const call = d.fetch ?? fetch
  // The one call this app makes as a client rather than a proxy, so it makes it with the
  // generated SDK. Everything else is bytes it has no opinion about.
  const api = createSandboxd({
    baseUrl: d.cfg.apiUrl,
    serviceToken: d.cfg.serviceToken,
    ...(d.fetch === undefined ? {} : { fetch: d.fetch }),
  })
  const down = (e: unknown) => unreachable(d.cfg.apiUrl, e)

  return new Hono({ strict: false })
    .basePath('/api')
    .get('/presets', (c) => c.json(d.presets.list()))
    .post('/sandboxes', async (c) => {
      const raw: unknown = await c.req.json().catch(() => {
        throw Err.badRequest('invalid JSON body')
      })
      const body = buildCreate({ defaultImage: d.cfg.defaultImage, presets: d.presets }, raw)
      const { data, error, response } = await createSandbox({ client: api, body })
      // The SDK never rejects. No response at all is transport: the API is down.
      if (!response) throw down(error)
      return c.json((data ?? error) as object, response.status as ContentfulStatusCode)
    })
    .all('/*', async (c) => {
      const path = c.req.path.slice('/api'.length) + new URL(c.req.url).search
      const url = new URL(path, d.cfg.apiUrl)
      const headers: Record<string, string> = {
        authorization: `Bearer ${d.cfg.serviceToken}`,
      }
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

/** A rejected fetch is transport: the API is down or restarting. Name it, so the page
 *  says so instead of "internal error". */
function unreachable(url: string, e: unknown) {
  log.warn('api unreachable', { url, err: String(e) })
  return new Err.Http(502, `api unreachable at ${url}`)
}

/** The API's answer, status and body untouched. */
const relay = (res: Pick<Response, 'body' | 'status' | 'headers'>) =>
  new Response(res.body, {
    status: res.status,
    headers: { 'content-type': res.headers.get('content-type') ?? 'application/json' },
  })
