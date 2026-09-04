// POST/GET/DELETE /sessions and the two token mints.
import { Hono } from 'hono'
import { z } from 'zod'
import type { SessionService } from '../../sessions'
import {
  AttachToken,
  CreateSession,
  Owner,
  PreviewRequest,
  PreviewToken,
  SessionView,
} from '../../schema'
import { Doc } from '../doc'

export function sessionRoutes(sessions: SessionService) {
  const doc = Doc.tag('Sessions')
  const owned = Doc.errors(400, 401, 404)
  return new Hono()
    .post(
      '/',
      doc(
        'Create a session',
        {
          201: Doc.json('Queued, or placed at once when a host has a free slot.', SessionView),
          ...Doc.errors(400, 401),
        },
        'Presets are resolved before this call, by the UI: the body names an image, a command and env.',
      ),
      Doc.validator('json', CreateSession),
      (c) => c.json(sessions.create(c.req.valid('json')), 201),
    )
    .get(
      '/',
      doc('List sessions', {
        200: Doc.json('Newest first.', z.array(SessionView)),
        ...Doc.errors(401),
      }),
      Doc.validator('query', Owner.partial()),
      (c) => c.json(sessions.list(c.req.valid('query').owner_id), 200),
    )
    .get(
      '/:id',
      doc('Get a session', { 200: Doc.json('The session.', SessionView), ...owned }),
      Doc.validator('query', Owner),
      (c) => c.json(sessions.get(c.req.param('id'), c.req.valid('query').owner_id), 200),
    )
    .delete(
      '/:id',
      doc('End a session', {
        200: Doc.json('The session, now ended or ending.', SessionView),
        ...owned,
      }),
      Doc.validator('query', Owner),
      (c) => c.json(sessions.cancel(c.req.param('id'), c.req.valid('query').owner_id), 200),
    )
    .post(
      '/:id/attach-token',
      doc('Mint an attach token', {
        200: Doc.json('Good for 60 s.', AttachToken),
        ...owned,
        ...Doc.errors(409),
      }),
      Doc.validator('json', Owner),
      (c) =>
        c.json(sessions.attachToken(c.req.param('id'), c.req.valid('json').owner_id), 200),
    )
    .post(
      '/:id/preview-token',
      doc('Mint a preview token', {
        200: Doc.json('Good for 10 min.', PreviewToken),
        ...owned,
      }),
      Doc.validator('json', PreviewRequest),
      (c) => {
        const b = c.req.valid('json')
        return c.json(sessions.previewToken(c.req.param('id'), b.owner_id, b.port), 200)
      },
    )
}
