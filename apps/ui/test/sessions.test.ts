import { expect, test } from 'bun:test'
import { resolve } from 'node:path'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'
import { buildCreate } from '../src/sessions'

const loaded = loadPresetDir(resolve(import.meta.dir, '../presets'))
const deps = (defaultImage: string | null = null) => ({
  defaultImage,
  presets: new PresetRegistry(loaded.presets),
  catalog: loaded.catalog,
})

test('preset picked by repo; caller image, cmd, idle and env win over the preset', () => {
  const b = buildCreate(deps(), {
    owner_id: 'me',
    repo: 'https://x/r.git',
    prompt: 'p',
    env: { EXTRA: '1', PROMPT: 'override' },
    secrets: { git_token: 'gt' },
    secret_env: { S: 'x' },
  })
  expect(b).toEqual({
    owner_id: 'me',
    image: 'sandboxd-coding-agent:latest',
    env: { REPO: 'https://x/r.git', PROMPT: 'override', EXTRA: '1' },
    secret_env: { GIT_TOKEN: 'gt', S: 'x' },
  })
  const j = buildCreate(deps(), { owner_id: 'me', preset: 'jupyter' })
  expect([j.image, j.cmd, j.idle_timeout_s]).toEqual([
    'sandboxd-jupyter:latest',
    undefined,
    4 * 3600,
  ])
  const c = buildCreate(deps(), {
    owner_id: 'me',
    preset: 'jupyter',
    image: 'mine',
    cmd: ['sh'],
    idle_timeout_s: 120,
  })
  expect([c.image, c.cmd, c.idle_timeout_s]).toEqual(['mine', ['sh'], 120])
})

test('custom: image from the caller, else the configured default, else a 400', () => {
  expect(buildCreate(deps('dflt:1'), { owner_id: 'me' }).image).toBe('dflt:1')
  expect(buildCreate(deps(), { owner_id: 'me', image: 'python:3.12' }).image).toBe(
    'python:3.12',
  )
  expect(() => buildCreate(deps(), { owner_id: 'me' })).toThrow(/implies no image/)
  expect(() => buildCreate(deps(), {})).toThrow(/owner_id is required/)
  expect(() => buildCreate(deps(), 'nope')).toThrow(/JSON object/)
  expect(() => buildCreate(deps(), { owner_id: 'me', preset: 'nope' })).toThrow(
    /preset must be one of/,
  )
  expect(() => buildCreate(deps(), { owner_id: 'me', image: 'i', env: { A: 1 } })).toThrow(
    /env.A must be a string/,
  )
})

test('services: catalog names and references become one compose document; full declarations pass through', () => {
  const b = buildCreate(deps(), {
    owner_id: 'me',
    image: 'i',
    services: [
      'postgres',
      { use: 'redis', name: 'cache', env: { X: '1' } },
      { name: 'mq', image: 'rabbitmq', ready: { port: 5672 } },
    ],
    compose: 'services:\n  app: { build: . }\n  db2: { image: "postgres:16" }\n',
  })
  expect(b.services).toEqual([{ name: 'mq', image: 'rabbitmq', ready: { port: 5672 } }])
  const doc = b.compose as { services: Record<string, Record<string, unknown>> }
  expect(Object.keys(doc.services)).toEqual(['postgres', 'cache', 'app', 'db2'])
  expect(doc.services.postgres!.image).toBe('postgres:16-alpine')
  expect(doc.services.cache).toMatchObject({
    image: 'redis:7-alpine',
    environment: { X: '1' },
  })
  expect(() =>
    buildCreate(deps(), { owner_id: 'me', image: 'i', services: ['mysql'] }),
  ).toThrow(/unknown service "mysql" \(catalog: postgres, redis\)/)
  expect(() =>
    buildCreate(deps(), { owner_id: 'me', image: 'i', services: ['postgres', 'postgres'] }),
  ).toThrow(/duplicated/)
  expect(() =>
    buildCreate(deps(), {
      owner_id: 'me',
      image: 'i',
      services: ['postgres'],
      compose: { services: { postgres: { image: 'x' } } },
    }),
  ).toThrow(/name "postgres" is duplicated/)
  expect(() => buildCreate(deps(), { owner_id: 'me', image: 'i', compose: 5 })).toThrow(
    /compose must be/,
  )
  expect(() => buildCreate(deps(), { owner_id: 'me', image: 'i', services: 'x' })).toThrow(
    /must be an array/,
  )
  // Nothing picked: the caller's document goes to the API untouched, whatever it is.
  expect(
    buildCreate(deps(), { owner_id: 'me', image: 'i', compose: 'services: 1' }).compose,
  ).toEqual({ services: 1 })
})
