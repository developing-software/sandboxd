// apps/api/src/http/handler/service.ts — the probe and the attach socket. Both public:
// the socket is gated by its own token, not the service token.
import { Hono } from 'hono'
import { unauthorized } from '@sandboxd/core/errors'
import type { Tokens } from '../../tokens'
import type { AttachData } from '../../attach'
import { AttachQuery, Ok } from '../../schema'
import { validator } from '../common'
import { describe, errors, json } from '../doc'

/** What Bun.serve hands `app.fetch` as the second argument. */
export interface Upgrader {
  upgrade(req: Request, opts: { data: AttachData }): boolean
}
export type Env = { Bindings: Upgrader }

export function serviceRoutes(tokens: Tokens) {
  const doc = describe('Service')
  return new Hono<Env>()
    .get('/healthz', doc('Liveness', { 200: json('Up.', Ok) }), (c) =>
      c.json({ ok: true } as const, 200),
    )
    .get(
      '/attach',
      doc(
        'Attach to a session terminal',
        {
          101: { description: 'WebSocket: binary frames are PTY bytes both ways.' },
          ...errors(401, 426),
        },
        'Browser endpoint. `token` comes from POST /sessions/:id/attach-token.',
      ),
      validator('query', AttachQuery),
      (c) => {
        const q = c.req.valid('query')
        const p = tokens.verify(q.token, 'attach')
        if (!p) throw unauthorized('invalid or expired attach token')
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
