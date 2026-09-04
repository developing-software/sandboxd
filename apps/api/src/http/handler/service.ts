// The probe and the attach socket. Both public: the socket is gated by its own token,
// not the service token.
import { Hono } from 'hono'
import { Err } from '@sandboxd/core/errors'
import type { Tokens } from '../../tokens'
import type { AttachData } from '../../attach'
import { AttachQuery, Ok } from '../../schema'
import { Doc } from '../doc'

/** What Bun.serve hands `app.fetch` as the second argument. */
export interface Upgrader {
  upgrade(req: Request, opts: { data: AttachData }): boolean
}
export type Bindings = { Bindings: Upgrader }

export function serviceRoutes(tokens: Tokens) {
  const doc = Doc.tag('Service')
  return new Hono<Bindings>()
    .get('/healthz', doc('Liveness', { 200: Doc.json('Up.', Ok) }), (c) =>
      c.json({ ok: true } as const, 200),
    )
    .get(
      '/attach',
      doc(
        'Attach to a session terminal',
        {
          101: { description: 'WebSocket: binary frames are PTY bytes both ways.' },
          ...Doc.errors(401, 426),
        },
        'Browser endpoint. `token` comes from POST /sessions/:id/attach-token.',
      ),
      Doc.validator('query', AttachQuery),
      (c) => {
        const q = c.req.valid('query')
        const p = tokens.verify(q.token, 'attach')
        if (!p) throw Err.unauthorized('invalid or expired attach token')
        const data: AttachData = {
          kind: 'attach',
          sid: p.sid,
          pty: null,
          cols: q.cols,
          rows: q.rows,
        }
        return c.env.upgrade(c.req.raw, { data })
          ? new Response(null, { status: 101 })
          : c.json({ error: 'websocket upgrade required' }, 426)
      },
    )
}
