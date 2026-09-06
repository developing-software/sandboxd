// The JSON half of the UI, under /api. Every route is one call on the generated SDK, so
// the browser never holds the token and this app never spells a URL or a shape the
// OpenAPI document did not give it. Two routes are its own: GET /presets, which the
// control plane does not have, and POST /sandboxes, which resolves a preset first.
import {
  type ApproveBody,
  approveHost,
  createSandbox,
  createSandboxd,
  endSandbox,
  getSandbox,
  listHosts,
  listSandboxes,
  mintAttachToken,
  mintPreviewToken,
  type OwnerBody,
  type PreviewBody,
  revokeHost,
} from '@sandboxd/sdk'
import { type Context, Hono } from 'hono'
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

/** What every generated call resolves to. The SDK never rejects. */
interface Result {
  data?: unknown
  error?: unknown
  response?: Response
}

export function createApi(d: ApiDeps) {
  const client = createSandboxd({
    baseUrl: d.cfg.apiUrl,
    serviceToken: d.cfg.serviceToken,
    ...(d.fetch === undefined ? {} : { fetch: d.fetch }),
  })
  // The API's answer, status and body untouched. No response at all is transport: the
  // API is down or restarting. Name it, so the page says so instead of "internal error".
  const reply = async (c: Context, result: Promise<Result>) => {
    const { data, error, response } = await result
    if (!response) {
      log.warn('api unreachable', { url: d.cfg.apiUrl, err: String(error) })
      throw new Err.Http(502, `api unreachable at ${d.cfg.apiUrl}`)
    }
    return c.json((data ?? error) as object, response.status as ContentfulStatusCode)
  }
  const id = (c: Context) => ({ id: c.req.param('id') ?? '' })
  const owner = (c: Context) => ({ owner_id: c.req.query('owner_id') ?? '' })

  return new Hono({ strict: false })
    .basePath('/api')
    .get('/presets', (c) => c.json(d.presets.list()))
    .get('/hosts', (c) => reply(c, listHosts({ client })))
    .post('/hosts/:id/approve', async (c) =>
      reply(c, approveHost({ client, path: id(c), body: await json<ApproveBody>(c) })),
    )
    .post('/hosts/:id/revoke', (c) => reply(c, revokeHost({ client, path: id(c) })))
    .get('/sandboxes', (c) => reply(c, listSandboxes({ client, query: owner(c) })))
    .post('/sandboxes', async (c) => {
      const deps = { defaultImage: d.cfg.defaultImage, presets: d.presets }
      const body = buildCreate(deps, await json<unknown>(c))
      return reply(c, createSandbox({ client, body }))
    })
    .get('/sandboxes/:id', (c) =>
      reply(c, getSandbox({ client, path: id(c), query: owner(c) })),
    )
    .delete('/sandboxes/:id', (c) =>
      reply(c, endSandbox({ client, path: id(c), query: owner(c) })),
    )
    .post('/sandboxes/:id/attach-token', async (c) =>
      reply(c, mintAttachToken({ client, path: id(c), body: await json<OwnerBody>(c) })),
    )
    .post('/sandboxes/:id/preview-token', async (c) =>
      reply(c, mintPreviewToken({ client, path: id(c), body: await json<PreviewBody>(c) })),
    )
    .onError(onError)
}

function onError(err: Error, c: Context) {
  if (err instanceof Err.Http)
    return c.json({ error: err.message }, err.status as ContentfulStatusCode)
  log.error('unhandled', { err: String(err), stack: err.stack })
  return c.json({ error: 'internal error' }, 500)
}

/** The body parsed and no more: the API validates the shape, not this app. */
const json = async <T>(c: Context) =>
  (await c.req.json().catch(() => {
    throw Err.badRequest('invalid JSON body')
  })) as T
