import { expect, test } from 'bun:test'
import { resolve } from 'node:path'
import { createApi } from '../src/api'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'

const loaded = loadPresetDir(resolve(import.meta.dir, '../presets'))

/** A fake API: records every request either SDK builds, answers with a fixed body. */
function setup(answer?: () => Response) {
  const calls: {
    method: string
    url: string
    auth: string | null
    owner: string | null
    body: unknown
  }[] = []
  const fetch = async (input: string | URL | Request, init?: RequestInit) => {
    const req = new Request(input, init)
    calls.push({
      method: req.method,
      url: req.url,
      auth: req.headers.get('authorization'),
      owner: req.headers.get('x-sandboxd-owner'),
      body: req.body ? await req.json() : null,
    })
    return answer?.() ?? Response.json({ id: 's_1', status: 'queued' }, { status: 201 })
  }
  const app = createApi({
    cfg: { apiUrl: 'http://api.test', serviceToken: 'secret', defaultImage: null },
    presets: new PresetRegistry(loaded.presets),
    fetch: fetch as typeof globalThis.fetch,
  })
  return { app, calls }
}

/** How a page calls: the owner is a header, on every request. */
const owner = { 'x-sandboxd-owner': 'me' }

test('the presets are served here, and nothing outside /api is', async () => {
  const { app, calls } = setup()
  const presets = (await (await app.request('/api/presets')).json()) as { name: string }[]
  expect(presets.map((p) => p.name)).toEqual([
    'agent',
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
    headers: { ...owner, 'content-type': 'application/json' },
    body: JSON.stringify({ repo: 'https://x/r.git' }),
  })
  expect(res.status).toBe(201)
  expect(await res.json()).toEqual({ id: 's_1', status: 'queued' })
  expect(calls).toHaveLength(1)
  expect(calls[0]).toEqual({
    method: 'POST',
    url: 'http://api.test/sandboxes',
    auth: 'Bearer secret',
    // The owner reaches the control plane beside the token, not inside the body.
    owner: 'me',
    body: {
      image: 'ghcr.io/developing-software/sandboxd-agent:latest',
      env: { REPO: 'https://x/r.git' },
      secret_env: {},
    },
  })
  const bad = await app.request('/api/sandboxes', {
    method: 'POST',
    headers: { ...owner, 'content-type': 'application/json' },
    body: JSON.stringify({ repo: 'r', agent: 'vim' }),
  })
  expect(bad.status).toBe(400)
  expect(await bad.json()).toEqual({
    error: 'agent must be one of claude, codex, opencode, shell',
  })
  expect((await app.request('/api/sandboxes', { method: 'POST', body: '{' })).status).toBe(400)
})

test('every other route is the generated call it names, with the token added', async () => {
  const { app, calls } = setup()
  const get = (path: string) => app.request(path, { headers: owner })
  const post = (path: string, body?: unknown) =>
    app.request(path, {
      method: 'POST',
      headers: { ...owner, 'content-type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  await get('/api/hosts')
  await post('/api/hosts/h1/approve', { code: 'ABCD' })
  await post('/api/hosts/h1/revoke')
  await get('/api/sandboxes')
  await get('/api/sandboxes/s_1')
  await app.request('/api/sandboxes/s_1', { method: 'DELETE', headers: owner })
  await post('/api/sandboxes/s_1/terminal')
  await post('/api/sandboxes/s_1/preview', { port: 3000 })
  expect(calls.map((c) => [c.method, c.url, c.body])).toEqual([
    ['GET', 'http://api.test/hosts', null],
    ['POST', 'http://api.test/hosts/h1/approve', { code: 'ABCD' }],
    ['POST', 'http://api.test/hosts/h1/revoke', null],
    ['GET', 'http://api.test/sandboxes', null],
    ['GET', 'http://api.test/sandboxes/s_1', null],
    ['DELETE', 'http://api.test/sandboxes/s_1', null],
    ['POST', 'http://api.test/sandboxes/s_1/terminal', null],
    ['POST', 'http://api.test/sandboxes/s_1/preview', { port: 3000 }],
  ])
  expect(new Set(calls.map((c) => c.auth))).toEqual(new Set(['Bearer secret']))
  // The five sandbox routes carry the owner; the three fleet ones have no owner to carry.
  expect(calls.filter((c) => c.owner === 'me')).toHaveLength(5)
  // No byte proxy: a path neither document names is a 404 here, not a call there.
  expect((await get('/api/healthz')).status).toBe(404)
  expect(calls).toHaveLength(8)
})

// approve and revoke answer 204, and there is nothing to relay: inventing a body would
// contradict the document that says there is none (decision 25).
test('a 204 comes back a 204, with no body', async () => {
  const { app } = setup(() => new Response(null, { status: 204 }))
  const res = await app.request('/api/hosts/h1/approve', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ code: 'ABCD' }),
  })
  expect(res.status).toBe(204)
  expect(await res.text()).toBe('')
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
  const list = await app.request('/api/sandboxes', { headers: owner })
  expect(list.status).toBe(502)
  expect(await list.json()).toEqual({ error: 'api unreachable at http://api.test' })
  const create = await app.request('/api/sandboxes', {
    method: 'POST',
    headers: { ...owner, 'content-type': 'application/json' },
    body: JSON.stringify({ image: 'i' }),
  })
  expect(create.status).toBe(502)
})
