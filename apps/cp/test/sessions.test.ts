import { expect, test } from 'bun:test'
import { resolve } from 'node:path'
import { Store } from '../src/store'
import { Scheduler } from '../src/scheduler'
import { Tokens } from '../src/tokens'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'
import { SessionService, validateEnv } from '../src/sessions'
import type { HostPlacement } from '../src/hosts/hub'
import type { SessionSpec } from '@sandboxd/core/messages'

class NoHosts implements HostPlacement {
  isOnline() {
    return false
  }
  capacity() {
    return null
  }
  createSession(_h: string, _s: SessionSpec) {
    return false
  }
  destroySession() {}
}

const loaded = loadPresetDir(resolve(import.meta.dir, '../../../presets'))

function setup(maxServices = 2) {
  const store = new Store(':memory:')
  const sched = new Scheduler(store, new NoHosts())
  const cfg = {
    defaultImage: 'default:latest',
    publicUrl: 'https://cp.example.com',
    previewDomain: 'preview.example.com',
    maxServices,
  }
  const presets = new PresetRegistry(loaded.presets)
  return {
    store,
    svc: new SessionService(
      cfg,
      store,
      new NoHosts(),
      sched,
      new Tokens('k'),
      presets,
      loaded.catalog,
    ),
  }
}

test('create: preset picked by repo, caller values win, secrets never stored', () => {
  const { store, svc } = setup()
  const v = svc.create({
    owner_id: 'me',
    repo: 'https://x/r.git',
    prompt: 'p',
    env: { EXTRA: '1', PROMPT: 'override' },
    secrets: { git_token: 'gt' },
    secret_env: { S: 'x' },
  })
  expect(v.preset).toBe('coding-agent')
  expect(v.status).toBe('queued')
  expect(v.queue_position).toBe(1)
  expect(v.image).toBe('sandboxd-coding-agent:latest')
  expect(v.env.PROMPT).toBe('override')
  expect(v.env.EXTRA).toBe('1')
  expect(v.cmd).toBeNull()
  const raw = JSON.stringify(store.db.query('SELECT * FROM sessions').all())
  expect(raw).not.toContain('gt')
  expect(raw).not.toContain('"S"')
})

test('create: preset defaults for image/cmd/idle apply unless the caller overrides; custom falls back to the CP default image', () => {
  const { svc } = setup()
  const j = svc.create({ owner_id: 'me', preset: 'jupyter' })
  expect(j.image).toBe('sandboxd-jupyter:latest')
  expect(j.cmd).toBeNull()
  expect(j.idle_timeout_s).toBe(4 * 3600)
  const c = svc.create({
    owner_id: 'me',
    preset: 'jupyter',
    image: 'mine',
    cmd: ['sh'],
    idle_timeout_s: 120,
  })
  expect([c.image, c.cmd, c.idle_timeout_s]).toEqual(['mine', ['sh'], 120])
  expect(svc.create({ owner_id: 'me' }).image).toBe('default:latest')
})

test('create: validation errors are 400s', () => {
  const { svc } = setup()
  expect(() => svc.create({})).toThrow(/owner_id is required/)
  expect(() => svc.create({ owner_id: 'me', cmd: [] })).toThrow(/cmd must be/)
  expect(() => svc.create({ owner_id: 'me', idle_timeout_s: 5 })).toThrow(/idle_timeout_s/)
  expect(() => svc.create({ owner_id: 'me', preset: 'nope' })).toThrow(/preset must be one of/)
  expect(() => svc.create({ owner_id: 'me', env: { TERM: 'x' } })).toThrow(/reserved/)
  expect(() => svc.create('nope')).toThrow(/JSON object/)
})

test('ownership: another owner sees 404, missing owner_id is 400; cancel and tokens', () => {
  const { svc } = setup()
  const v = svc.create({ owner_id: 'me' })
  expect(svc.get(v.id, 'me').id).toBe(v.id)
  expect(() => svc.get(v.id, 'you')).toThrow(/session not found/)
  expect(() => svc.get(v.id, null)).toThrow(/owner_id is required/)
  expect(() => svc.attachToken(v.id, 'me')).toThrow(/session is queued/)
  const p = svc.previewToken(v.id, 'me', 3000)
  expect(p.url).toBe(`https://3000-${v.id}.preview.example.com/?t=${p.token}`)
  expect(() => svc.previewToken(v.id, 'me', 0)).toThrow(/port must be/)
  expect(svc.cancel(v.id, 'me').status).toBe('ended')
  expect(svc.list('me')).toHaveLength(1)
  expect(svc.list('you')).toHaveLength(0)
})

test('validateEnv: names, reserved keys, types', () => {
  expect(validateEnv('env', undefined)).toEqual({})
  expect(validateEnv('env', { A_1: 'x' })).toEqual({ A_1: 'x' })
  expect(() => validateEnv('env', { 'bad-name': 'x' })).toThrow(/invalid variable name/)
  expect(() => validateEnv('env', { TERM: 'x' })).toThrow(/reserved/)
  expect(() => validateEnv('env', { A: 1 })).toThrow(/must be a string/)
  expect(() => validateEnv('env', ['A'])).toThrow(/object/)
})

test('services: inline declarations validated, secrets split by name, non-secret half visible in the view', () => {
  const { store, svc } = setup()
  const v = svc.create({
    owner_id: 'me',
    services: [
      {
        name: 'db',
        image: 'postgres:16',
        env: { POSTGRES_DB: 'app' },
        secret_env: { POSTGRES_PASSWORD: 'pw' },
        ready: { port: 5432 },
      },
      { name: 'cache', image: 'redis:7', cmd: ['redis-server', '--appendonly', 'yes'] },
    ],
  })
  expect(v.services).toEqual([
    {
      name: 'db',
      image: 'postgres:16',
      env: { POSTGRES_DB: 'app' },
      cmd: null,
      ready: { port: 5432, timeout_s: 60 },
    },
    {
      name: 'cache',
      image: 'redis:7',
      env: {},
      cmd: ['redis-server', '--appendonly', 'yes'],
      ready: null,
    },
  ])
  expect(JSON.stringify(store.db.query('SELECT * FROM sessions').all())).not.toContain('pw')
  const bad = (services: unknown) => () => svc.create({ owner_id: 'me', services })
  expect(bad('x')).toThrow(/must be an array/)
  expect(bad([{ name: 'Bad_Name', image: 'i' }])).toThrow(/name must match/)
  expect(bad([{ name: 'sandbox', image: 'i' }])).toThrow(/reserved/)
  expect(
    bad([
      { name: 'a', image: 'i' },
      { name: 'a', image: 'i' },
    ]),
  ).toThrow(/duplicated/)
  expect(bad([{ name: 'a' }])).toThrow(/image is required/)
  expect(bad([{ name: 'a', image: 'i', cmd: [] }])).toThrow(/cmd must be a non-empty array/)
  expect(bad([{ name: 'a', image: 'i', ready: { port: 0 } }])).toThrow(/ready.port/)
  expect(bad([{ name: 'a', image: 'i', ready: { port: 80, timeout_s: 9999 } }])).toThrow(
    /ready.timeout_s/,
  )
  expect(bad([{ name: 'a', image: 'i', env: { TERM: 'x' } }])).toThrow(/reserved/)
  expect(
    bad([
      { name: 'a', image: 'i' },
      { name: 'b', image: 'i' },
      { name: 'c', image: 'i' },
    ]),
  ).toThrow(/at most 2/)
})

test('services: catalog names bring their sandbox env; caller env wins; references can rename and override', () => {
  const { svc } = setup()
  const v = svc.create({
    owner_id: 'me',
    repo: 'https://x/r.git',
    services: ['postgres'],
    env: { PGDATABASE: 'mine' },
  })
  expect(v.services.map((s) => [s.name, s.image, s.ready])).toEqual([
    ['postgres', 'postgres:16-alpine', { port: 5432, timeout_s: 90 }],
  ])
  expect(v.env).toMatchObject({
    REPO: 'https://x/r.git',
    DATABASE_URL: 'postgres://sandboxd:sandboxd@postgres:5432/app',
    PGHOST: 'postgres',
    PGDATABASE: 'mine',
  })
  const w = svc.create({
    owner_id: 'me',
    services: [{ use: 'redis', name: 'cache', env: { X: '1' } }],
  })
  expect(w.services[0]).toMatchObject({
    name: 'cache',
    image: 'redis:7-alpine',
    env: { X: '1' },
  })
  expect(w.env.REDIS_URL).toBe('redis://redis:6379') // literal from the catalog: renaming does not rewrite URLs
  expect(() => svc.create({ owner_id: 'me', services: ['mysql'] })).toThrow(
    /unknown service "mysql" \(catalog: postgres, redis\)/,
  )
  expect(() =>
    svc.create({ owner_id: 'me', services: [{ use: 'redis', name: 'Bad' }] }),
  ).toThrow(/name must match/)
})

test('services: a compose document is accepted as-is; sources merge in order and share the cap', () => {
  const { svc } = setup(3)
  const compose = `
services:
  app: { build: ., depends_on: [db] }
  db: { image: "postgres:16", environment: { POSTGRES_PASSWORD: pw }, ports: ["5432:5432"], command: postgres -c log_statement=all }
`
  const v = svc.create({ owner_id: 'me', services: ['redis'], compose })
  expect(v.services.map((s) => s.name)).toEqual(['redis', 'db'])
  expect(v.services[1]).toEqual({
    name: 'db',
    image: 'postgres:16',
    env: { POSTGRES_PASSWORD: 'pw' },
    cmd: ['postgres', '-c', 'log_statement=all'],
    ready: { port: 5432, timeout_s: 60 },
  })
  expect(
    svc.create({ owner_id: 'me', compose: { services: { db: { image: 'i' } } } }).services,
  ).toHaveLength(1)
  expect(() =>
    svc.create({
      owner_id: 'me',
      services: ['postgres'],
      compose: { services: { postgres: { image: 'i' } } },
    }),
  ).toThrow(/name "postgres" is duplicated/)
  expect(() =>
    svc.create({
      owner_id: 'me',
      services: ['postgres', 'redis'],
      compose: { services: { a: { image: 'i' }, b: { image: 'i' } } },
    }),
  ).toThrow(/at most 3/)
  expect(() =>
    svc.create({
      owner_id: 'me',
      compose: { services: { a: { image: 'i', privileged: true } } },
    }),
  ).toThrow(/privileged is not supported/)
  expect(() => svc.create({ owner_id: 'me', compose: 5 })).toThrow(
    /must be a compose document/,
  )
})
