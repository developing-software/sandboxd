import { expect, test } from 'bun:test'
import { Store } from '../src/store'
import { Scheduler } from '../src/scheduler'
import { Tokens } from '../src/tokens'
import { SessionService } from '../src/sessions'
import { HostService } from '../src/hosts/service'
import { createApp } from '../src/http/index'
import type { HostPlacement, HostPresence } from '../src/hosts/hub'

class NoHosts implements HostPlacement, HostPresence {
  approved: string[] = []
  isOnline() {
    return false
  }
  capacity() {
    return null
  }
  createSession() {
    return false
  }
  destroySession() {}
  notifyApproved(id: string) {
    this.approved.push(id)
  }
}

function setup() {
  const store = new Store(':memory:')
  const hub = new NoHosts()
  const sched = new Scheduler(store, hub)
  const tokens = new Tokens('k')
  const cfg = {
    publicUrl: 'https://cp.example.com',
    previewDomain: 'preview.example.com',
  }
  const app = createApp({
    serviceToken: 'secret',
    tokens,
    hosts: new HostService(store, hub),
    sessions: new SessionService(cfg, store, hub, sched, tokens),
  })
  const auth = { authorization: 'Bearer secret' }
  const json = (method: string, path: string, body: unknown, headers = auth) =>
    app.request(path, {
      method,
      headers: { ...headers, 'content-type': 'application/json' },
      body: JSON.stringify(body),
    })
  return { app, store, hub, tokens, auth, json }
}

test('auth: healthz and the document are open, everything else needs the service token', async () => {
  const { app, auth } = setup()
  expect((await app.request('/healthz')).status).toBe(200)
  expect(await (await app.request('/healthz')).json()).toEqual({ ok: true })
  expect((await app.request('/openapi.json')).status).toBe(200)
  expect((await app.request('/sessions')).status).toBe(401)
  expect(
    (await app.request('/hosts', { headers: { authorization: 'Bearer nope' } })).status,
  ).toBe(401)
  expect((await app.request('/hosts', { headers: auth })).status).toBe(200)
  expect((await app.request('/hosts/', { headers: auth })).status).toBe(200)
  const missing = await app.request('/nope', { headers: auth })
  expect(missing.status).toBe(404)
  expect(await missing.json()).toEqual({ error: 'route not found' })
})

test('POST /sessions: the schema rejects bad bodies with one entry per field, and unknown keys', async () => {
  const { json } = setup()
  const empty = await json('POST', '/sessions', {})
  expect(empty.status).toBe(400)
  const e = (await empty.json()) as { error: string; issues: { path?: string }[] }
  expect(e.issues.map((i) => i.path).toSorted()).toEqual(['image', 'owner_id'])
  expect(e.error).toMatch(/^owner_id: /)

  const preset = await json('POST', '/sessions', { owner_id: 'me', image: 'i', repo: 'x' })
  expect(preset.status).toBe(400)
  expect(((await preset.json()) as { error: string }).error).toMatch(/repo/)

  const bad = async (body: unknown, re: RegExp) => {
    const r = await json('POST', '/sessions', body)
    expect(r.status).toBe(400)
    expect(((await r.json()) as { error: string }).error).toMatch(re)
  }
  await bad({ owner_id: 'me', image: 'i', cmd: [] }, /cmd/)
  await bad({ owner_id: 'me', image: 'i', idle_timeout_s: 5 }, /idle_timeout_s/)
  await bad({ owner_id: 'me', image: 'i', env: { TERM: 'x' } }, /reserved/)
  await bad({ owner_id: 'me', image: 'i', env: { 'bad-name': 'x' } }, /invalid variable name/)
  await bad({ owner_id: 'me', image: 'i', services: [] }, /services/)
})

test('sessions: create, list, get, ownership, delete, tokens', async () => {
  const { app, json, auth, store } = setup()
  const created = await json('POST', '/sessions', {
    owner_id: 'me',
    image: 'img:1',
    cmd: ['bash', '-l'],
    env: { A: '1' },
    secret_env: { S: 'shh' },
  })
  expect(created.status).toBe(201)
  const s = (await created.json()) as { id: string; status: string }
  expect(s.status).toBe('queued')
  const raw = JSON.stringify(store.db.query('SELECT * FROM sessions').all())
  expect(raw).not.toContain('shh')

  const list = await app.request('/sessions?owner_id=me', { headers: auth })
  expect(((await list.json()) as unknown[]).length).toBe(1)
  expect(
    (
      (await (
        await app.request('/sessions?owner_id=you', { headers: auth })
      ).json()) as unknown[]
    ).length,
  ).toBe(0)

  expect((await app.request(`/sessions/${s.id}?owner_id=me`, { headers: auth })).status).toBe(
    200,
  )
  expect((await app.request(`/sessions/${s.id}?owner_id=you`, { headers: auth })).status).toBe(
    404,
  )
  expect((await app.request(`/sessions/${s.id}`, { headers: auth })).status).toBe(400)

  const attach = await json('POST', `/sessions/${s.id}/attach-token`, { owner_id: 'me' })
  expect(attach.status).toBe(409)
  const preview = await json('POST', `/sessions/${s.id}/preview-token`, {
    owner_id: 'me',
    port: 3000,
  })
  expect(preview.status).toBe(200)
  const p = (await preview.json()) as { url: string; token: string }
  expect(p.url).toBe(`https://3000-${s.id}.preview.example.com/?t=${p.token}`)
  expect(
    (await json('POST', `/sessions/${s.id}/preview-token`, { owner_id: 'me', port: 70000 }))
      .status,
  ).toBe(400)

  const ended = await app.request(`/sessions/${s.id}?owner_id=me`, {
    method: 'DELETE',
    headers: auth,
  })
  expect(((await ended.json()) as { status: string }).status).toBe('ended')
})

test('GET /attach: token-gated upgrade through the server the runtime hands in', async () => {
  const { app, tokens } = setup()
  expect((await app.request('/attach')).status).toBe(400)
  expect((await app.request('/attach?token=nope')).status).toBe(401)
  const token = tokens.sign({ k: 'attach', sid: 's_1' }, 60_000)
  const upgrades: unknown[] = []
  const env = {
    upgrade(_req: Request, opts: { data: unknown }) {
      upgrades.push(opts.data)
      return true
    },
  }
  const res = await app.request(`/attach?token=${token}&cols=80&rows=24`, {}, env)
  expect(res.status).toBe(101)
  expect(upgrades).toEqual([{ kind: 'attach', sid: 's_1', pty: null, cols: 80, rows: 24 }])
  expect(
    (await app.request(`/attach?token=${token}`, {}, { upgrade: () => false })).status,
  ).toBe(426)
})

test('hosts: approve needs the printed code; the document lists every route', async () => {
  const { app, json, auth, store, hub } = setup()
  store.insertPendingHost({
    id: 'h1',
    name: 'box',
    fingerprint: 'fp',
    approve_code: 'ABCD-EF',
    max_sessions: 2,
  })
  expect((await json('POST', '/hosts/h1/approve', {})).status).toBe(400)
  expect((await json('POST', '/hosts/h1/approve', { code: 'nope' })).status).toBe(400)
  expect((await json('POST', '/hosts/h1/approve', { code: 'abcd-ef' })).status).toBe(200)
  expect(hub.approved).toEqual(['h1'])
  expect((await json('POST', '/hosts/h1/approve', { code: 'abcd-ef' })).status).toBe(409)
  expect((await json('POST', '/hosts/nope/revoke', {})).status).toBe(404)
  const hosts = (await (await app.request('/hosts', { headers: auth })).json()) as {
    status: string
  }[]
  expect(hosts.map((h) => h.status)).toEqual(['approved'])

  const doc = (await (await app.request('/openapi.json')).json()) as {
    paths: Record<string, object>
  }
  expect(Object.keys(doc.paths).toSorted()).toEqual([
    '/attach',
    '/healthz',
    '/hosts',
    '/hosts/{id}/approve',
    '/hosts/{id}/revoke',
    '/sessions',
    '/sessions/{id}',
    '/sessions/{id}/attach-token',
    '/sessions/{id}/preview-token',
  ])
})
