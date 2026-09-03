// apps/api/src/http/handler/host.ts — worker hosts: list, approve, revoke.
import { Hono } from 'hono'
import { z } from 'zod'
import type { HostService } from '../../hosts/service'
import { ApproveHost, HostView, Ok } from '../../schema'
import { validator } from '../common'
import { describe, errors, json } from '../doc'

export function hostRoutes(hosts: HostService) {
  const doc = describe('Hosts')
  return new Hono()
    .get(
      '/',
      doc('List hosts', {
        200: json('Every enrolled host.', z.array(HostView)),
        ...errors(401),
      }),
      (c) => c.json(hosts.list(), 200),
    )
    .post(
      '/:id/approve',
      doc(
        'Approve a pending host',
        { 200: json('Approved.', Ok), ...errors(400, 401, 404, 409) },
        'The code is what the worker printed when it enrolled.',
      ),
      validator('json', ApproveHost),
      (c) => {
        hosts.approve(c.req.param('id'), c.req.valid('json').code)
        return c.json({ ok: true } as const, 200)
      },
    )
    .post(
      '/:id/revoke',
      doc('Revoke a host', { 200: json('Revoked.', Ok), ...errors(401, 404) }),
      (c) => {
        hosts.revoke(c.req.param('id'))
        return c.json({ ok: true } as const, 200)
      },
    )
}
