// apps/api/src/http/handler/session.ts — POST/GET/DELETE /sessions and the two token mints.
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
import { validator } from '../common'
import { describe, errors, json } from '../doc'

export function sessionRoutes(sessions: SessionService) {
  const doc = describe('Sessions')
  const owned = errors(400, 401, 404)
  return new Hono()
    .post(
      '/',
      doc(
        'Create a session',
        {
          201: json('Queued, or placed at once when a host has a free slot.', SessionView),
          ...errors(400, 401),
        },
        'Presets are resolved before this call, by the UI: the body names an image, a command and env.',
      ),
      validator('json', CreateSession),
      (c) => c.json(sessions.create(c.req.valid('json')), 201),
    )
    .get(
      '/',
      doc('List sessions', {
        200: json('Newest first.', z.array(SessionView)),
        ...errors(401),
      }),
      validator('query', Owner.partial()),
      (c) => c.json(sessions.list(c.req.valid('query').owner_id), 200),
    )
    .get(
      '/:id',
      doc('Get a session', { 200: json('The session.', SessionView), ...owned }),
      validator('query', Owner),
      (c) => c.json(sessions.get(c.req.param('id'), c.req.valid('query').owner_id), 200),
    )
    .delete(
      '/:id',
      doc('End a session', {
        200: json('The session, now ended or ending.', SessionView),
        ...owned,
      }),
      validator('query', Owner),
      (c) => c.json(sessions.cancel(c.req.param('id'), c.req.valid('query').owner_id), 200),
    )
    .post(
      '/:id/attach-token',
      doc('Mint an attach token', {
        200: json('Good for 60 s.', AttachToken),
        ...owned,
        ...errors(409),
      }),
      validator('json', Owner),
      (c) =>
        c.json(sessions.attachToken(c.req.param('id'), c.req.valid('json').owner_id), 200),
    )
    .post(
      '/:id/preview-token',
      doc('Mint a preview token', { 200: json('Good for 10 min.', PreviewToken), ...owned }),
      validator('json', PreviewRequest),
      (c) => {
        const b = c.req.valid('json')
        return c.json(sessions.previewToken(c.req.param('id'), b.owner_id, b.port), 200)
      },
    )
}
