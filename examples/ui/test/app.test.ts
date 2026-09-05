import { expect, test } from 'bun:test'
import { resolve } from 'node:path'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'
import { createApp } from '../src/app'

const loaded = loadPresetDir(resolve(import.meta.dir, '../presets'))

/** A fake API: records every request, answers with a fixed body. */
function setup() {
  const calls: { method: string; url: string; auth: string | null; body: unknown }[] = []
  const fetch = async (input: string | URL | Request, init?: RequestInit) => {
    const req = new Request(String(input instanceof Request ? input.url : input), init)
    calls.push({
      method: req.method,
      url: req.url,
      auth: req.headers.get('authorization'),
      body: req.body ? await req.json() : null,
    })
    return Response.json({ id: 's_1', status: 'queued' }, { status: 201 })
  }
  const app = createApp({
    cfg: { apiUrl: 'http://api.test', serviceToken: 'secret', defaultImage: null },
    presets: new PresetRegistry(loaded.presets),
    html: async () => '<h1>ui</h1>',
    fetch: fetch as typeof globalThis.fetch,
  })
  return { app, calls }
}

test('the page and the presets are served here', async () => {
  const { app, calls } = setup()
  expect(await (await app.request('/')).text()).toBe('<h1>ui</h1>')
  const presets = (await (await app.request('/presets')).json()) as { name: string }[]
  expect(presets.map((p) => p.name)).toEqual(['coding-agent', 'custom', 'jupyter', 'vscode'])
  expect(calls).toEqual([])
})

test('POST /sandboxes resolves the preset, then calls the API with the token', async () => {
  const { app, calls } = setup()
  const res = await app.request('/sandboxes', {
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
  const bad = await app.request('/sandboxes', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ owner_id: 'me', repo: 'r', agent: 'vim' }),
  })
  expect(bad.status).toBe(400)
  expect(await bad.json()).toEqual({
    error: 'agent must be one of claude, codex, opencode, shell',
  })
  expect((await app.request('/sandboxes', { method: 'POST', body: '{' })).status).toBe(400)
})

test('everything else is forwarded to the API with the token added', async () => {
  const { app, calls } = setup()
  await app.request('/sandboxes?owner_id=me')
  await app.request('/sandboxes/s_1/attach-token', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ owner_id: 'me' }),
  })
  await app.request('/hosts/h1/revoke', { method: 'POST' })
  expect(calls.map((c) => [c.method, c.url, c.auth, c.body])).toEqual([
    ['GET', 'http://api.test/sandboxes?owner_id=me', 'Bearer secret', null],
    [
      'POST',
      'http://api.test/sandboxes/s_1/attach-token',
      'Bearer secret',
      { owner_id: 'me' },
    ],
    ['POST', 'http://api.test/hosts/h1/revoke', 'Bearer secret', null],
  ])
})

test('an unreachable API is a 502 that names it, not an internal error', async () => {
  const app = createApp({
    cfg: { apiUrl: 'http://api.test', serviceToken: 'secret', defaultImage: null },
    presets: new PresetRegistry(loaded.presets),
    html: async () => '',
    fetch: (async () => {
      throw new Error('Unable to connect. Is the computer able to access the url?')
    }) as unknown as typeof globalThis.fetch,
  })
  const list = await app.request('/sandboxes?owner_id=me')
  expect(list.status).toBe(502)
  expect(await list.json()).toEqual({ error: 'api unreachable at http://api.test' })
  const create = await app.request('/sandboxes', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ owner_id: 'me', image: 'i' }),
  })
  expect(create.status).toBe(502)
})
