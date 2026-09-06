import { expect, test } from 'bun:test'
import { resolve } from 'node:path'
import { createApi } from '../src/api'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'

const loaded = loadPresetDir(resolve(import.meta.dir, '../presets'))

/** A fake API: records every request the SDK builds, answers with a fixed body. */
function setup() {
  const calls: { method: string; url: string; auth: string | null; body: unknown }[] = []
  const fetch = async (input: string | URL | Request, init?: RequestInit) => {
    const req = new Request(input, init)
    calls.push({
      method: req.method,
      url: req.url,
      auth: req.headers.get('authorization'),
      body: req.body ? await req.json() : null,
    })
    return Response.json({ id: 's_1', status: 'queued' }, { status: 201 })
  }
  const app = createApi({
    cfg: { apiUrl: 'http://api.test', serviceToken: 'secret', defaultImage: null },
    presets: new PresetRegistry(loaded.presets),
    fetch: fetch as typeof globalThis.fetch,
  })
  return { app, calls }
}

test('the presets are served here, and nothing outside /api is', async () => {
  const { app, calls } = setup()
  const presets = (await (await app.request('/api/presets')).json()) as { name: string }[]
  expect(presets.map((p) => p.name)).toEqual([
    'coding-agent',
    'custom',
    'http',
    'jupyter',
    'node',
    'notebook',
    'python',
    'ubuntu',
    'vscode',
  ])
  expect((await app.request('/presets')).status).toBe(404)
  expect(calls).toEqual([])
})

test('POST /api/sandboxes resolves the preset, then calls the API with the token', async () => {
  const { app, calls } = setup()
  const res = await app.request('/api/sandboxes', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ owner_id: 'me', repo: 'https://x/r.git' }),
  })
  expect(res.status).toBe(201)
  expect(await res.json()).toEqual({ id: 's_1', status: 'queued' })
  expect(calls).toHaveLength(1)
  expect(calls[0]).toEqual({
    method: 'POST',
    url: 'http://api.test/sandboxes',
    auth: 'Bearer secret',
    body: {
      owner_id: 'me',
      image: 'sandboxd-coding-agent:latest',
      env: { REPO: 'https://x/r.git' },
      secret_env: {},
    },
  })
  const bad = await app.request('/api/sandboxes', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ owner_id: 'me', repo: 'r', agent: 'vim' }),
  })
  expect(bad.status).toBe(400)
  expect(await bad.json()).toEqual({
    error: 'agent must be one of claude, codex, opencode, shell',
  })
  expect((await app.request('/api/sandboxes', { method: 'POST', body: '{' })).status).toBe(400)
})

test('every other route is the SDK call it names, with the token added', async () => {
  const { app, calls } = setup()
  const post = (path: string, body?: unknown) =>
    app.request(path, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  await app.request('/api/hosts')
  await post('/api/hosts/h1/approve', { code: 'ABCD' })
  await post('/api/hosts/h1/revoke')
  await app.request('/api/sandboxes?owner_id=me')
  await app.request('/api/sandboxes/s_1?owner_id=me')
  await app.request('/api/sandboxes/s_1?owner_id=me', { method: 'DELETE' })
  await post('/api/sandboxes/s_1/attach-token', { owner_id: 'me' })
  await post('/api/sandboxes/s_1/preview-token', { owner_id: 'me', port: 3000 })
  expect(calls.map((c) => [c.method, c.url, c.body])).toEqual([
    ['GET', 'http://api.test/hosts', null],
    ['POST', 'http://api.test/hosts/h1/approve', { code: 'ABCD' }],
    ['POST', 'http://api.test/hosts/h1/revoke', null],
    ['GET', 'http://api.test/sandboxes?owner_id=me', null],
    ['GET', 'http://api.test/sandboxes/s_1?owner_id=me', null],
    ['DELETE', 'http://api.test/sandboxes/s_1?owner_id=me', null],
    ['POST', 'http://api.test/sandboxes/s_1/attach-token', { owner_id: 'me' }],
    ['POST', 'http://api.test/sandboxes/s_1/preview-token', { owner_id: 'me', port: 3000 }],
  ])
  expect(new Set(calls.map((c) => c.auth))).toEqual(new Set(['Bearer secret']))
  // No byte proxy: a path the document does not name is a 404 here, not a call there.
  expect((await app.request('/api/healthz')).status).toBe(404)
  expect(calls).toHaveLength(8)
})

test('an unreachable API is a 502 that names it, not an internal error', async () => {
  const app = createApi({
    cfg: { apiUrl: 'http://api.test', serviceToken: 'secret', defaultImage: null },
    presets: new PresetRegistry(loaded.presets),
    fetch: (() =>
      Promise.reject(
        new Error('Unable to connect. Is the computer able to access the url?'),
      )) as unknown as typeof globalThis.fetch,
  })
  const list = await app.request('/api/sandboxes?owner_id=me')
  expect(list.status).toBe(502)
  expect(await list.json()).toEqual({ error: 'api unreachable at http://api.test' })
  const create = await app.request('/api/sandboxes', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ owner_id: 'me', image: 'i' }),
  })
  expect(create.status).toBe(502)
})
