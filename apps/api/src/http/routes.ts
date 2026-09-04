// Mounting only — one handler per resource under ./handler. The bearer check runs first
// and skips the few public paths; everything after it is the parent app's.
import { Hono, type MiddlewareHandler } from 'hono'
import type { ContentfulStatusCode } from 'hono/utils/http-status'
import { Err } from '@sandboxd/core/errors'
import { Log } from '@sandboxd/core/log'
import type { Tokens } from '../tokens'
import type { HostService } from '../hosts/service'
import type { SessionService } from '../sessions'
import { hostRoutes } from './handler/host'
import { sessionRoutes } from './handler/session'
import { serviceRoutes, type Bindings } from './handler/service'

const log = Log.create('http')

/** Open to anyone: the probe, the token-gated socket, and the document. */
const PUBLIC = new Set(['/healthz', '/attach', '/openapi.json', '/doc'])

const bearer =
  (token: string): MiddlewareHandler =>
  async (c, next) => {
    if (PUBLIC.has(c.req.path)) return next()
    if (c.req.header('authorization') !== `Bearer ${token}`) throw Err.unauthorized()
    return next()
  }

export interface Deps {
  serviceToken: string
  tokens: Tokens
  hosts: HostService
  sessions: SessionService
}

export function createRoutes(deps: Deps) {
  return (
    new Hono<Bindings>({ strict: false })
      .use('*', bearer(deps.serviceToken))
      .route('/', serviceRoutes(deps.tokens))
      .route('/hosts', hostRoutes(deps.hosts))
      .route('/sessions', sessionRoutes(deps.sessions))
      .notFound((c) => c.json({ error: 'route not found' }, 404))
      // Every thrown value leaves as the same shape. Err.Http carries its status; anything
      // else is a bug and says nothing beyond "internal".
      .onError((err, c) => {
        if (err instanceof Err.Http)
          return c.json({ error: err.message }, err.status as ContentfulStatusCode)
        log.error('unhandled', { err: String(err), stack: err.stack })
        return c.json({ error: 'internal error' }, 500)
      })
  )
}

/** The typed surface `hono/client` consumes in apps/ui. */
export type Routes = ReturnType<typeof createRoutes>
