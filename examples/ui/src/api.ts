// The JSON half of the UI, under /api. Every route is one generated call, so the browser
// never holds the token and this app never spells a URL or a shape a document did not give
// it. Two routes are its own: GET /presets, which the control plane does not have, and
// POST /sandboxes, which resolves a preset first.
//
// Two generated trees, because there are two documents. `@sandboxd/sdk` is the client
// contract; `./admin` is generated here from api/admin.yaml, which has no published client
// (DESIGN.md decision 27).
import {
  createSandbox,
  createSandboxd,
  endSandbox,
  getSandbox,
  listSandboxes,
  openPreview,
  openTerminal,
  type PreviewBody,
} from '@sandboxd/sdk'
import { type Context, Hono } from 'hono'
import type { ContentfulStatusCode } from 'hono/utils/http-status'
import {
  type ApproveBody,
  approveHost,
  type ClientOptions,
  listHosts,
  revokeHost,
} from './admin'
import { createClient, createConfig } from './admin/client'
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
  const { client, admin } = clients(d)
  const reply = relay(d.cfg.apiUrl)

  return new Hono({ strict: false })
    .basePath('/api')
    .get('/presets', (c) => c.json(d.presets.list()))
    .get('/hosts', (c) => reply(c, listHosts({ client: admin })))
    .post('/hosts/:id/approve', async (c) =>
      reply(c, approveHost({ client: admin, path: id(c), body: await json<ApproveBody>(c) })),
    )
    .post('/hosts/:id/revoke', (c) => reply(c, revokeHost({ client: admin, path: id(c) })))
    .get('/sandboxes', (c) => reply(c, listSandboxes({ client, headers: owner(c) })))
    .post('/sandboxes', async (c) => {
      const deps = { defaultImage: d.cfg.defaultImage, presets: d.presets }
      const body = buildCreate(deps, await json<unknown>(c))
      return reply(c, createSandbox({ client, headers: owner(c), body }))
    })
    .get('/sandboxes/:id', (c) =>
      reply(c, getSandbox({ client, path: id(c), headers: owner(c) })),
    )
    .delete('/sandboxes/:id', (c) =>
      reply(c, endSandbox({ client, path: id(c), headers: owner(c) })),
    )
    .post('/sandboxes/:id/terminal', (c) =>
      reply(c, openTerminal({ client, path: id(c), headers: owner(c) })),
    )
    .post('/sandboxes/:id/preview', async (c) =>
      reply(
        c,
        openPreview({
          client,
          path: id(c),
          headers: owner(c),
          body: await json<PreviewBody>(c),
        }),
      ),
    )
    .onError(onError)
}

/**
 * Two clients on one listener, because there are two documents. They carry the same token
 * today; giving the operator surface its own is a second field in the config and a
 * different string here, and nothing else.
 */
function clients(d: ApiDeps) {
  const transport = d.fetch === undefined ? {} : { fetch: d.fetch }
  const config = { baseUrl: d.cfg.apiUrl, ...transport }
  return {
    client: createSandboxd({ ...config, serviceToken: d.cfg.serviceToken }),
    admin: createClient(
      createConfig<ClientOptions>({ ...config, auth: () => d.cfg.serviceToken }),
    ),
  }
}

/** What every generated call resolves to. Neither SDK ever rejects. */
interface Result {
  data?: unknown
  error?: unknown
  response?: Response
}

/**
 * The API's answer, status and body untouched. No response at all is transport: the API is
 * down or restarting. Name it, so the page says so instead of "internal error".
 */
const relay = (apiUrl: string) => async (c: Context, result: Promise<Result>) => {
  const { data, error, response } = await result
  if (!response) {
    log.warn('api unreachable', { url: apiUrl, err: String(error) })
    throw new Err.Http(502, `api unreachable at ${apiUrl}`)
  }
  // 204 is the answer to approve and revoke: a body would be inventing one.
  if (response.status === 204) return c.body(null, 204)
  return c.json((data ?? error) as object, response.status as ContentfulStatusCode)
}

const id = (c: Context) => ({ id: c.req.param('id') ?? '' })

/**
 * The owner travels as a header here too, because it does on the API this wraps. An absent
 * one reaches the control plane empty and is refused there: what the API owns, the API
 * validates.
 */
const owner = (c: Context) => ({ 'X-Sandboxd-Owner': c.req.header('x-sandboxd-owner') ?? '' })

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
