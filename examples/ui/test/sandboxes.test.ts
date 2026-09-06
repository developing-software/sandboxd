import { expect, test } from 'bun:test'
import { resolve } from 'node:path'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'
import { buildCreate } from '../src/sandboxes'

const loaded = loadPresetDir(resolve(import.meta.dir, '../presets'))
const deps = (defaultImage: string | null = null) => ({
  defaultImage,
  presets: new PresetRegistry(loaded.presets),
})

// The owner is absent from every case below because it is no longer part of this body: it
// is a header on the call, so it never reaches buildCreate (decision 24).

test('preset picked by repo; caller image, cmd, idle and env win over the preset', () => {
  const b = buildCreate(deps(), {
    repo: 'https://x/r.git',
    prompt: 'p',
    env: { EXTRA: '1', PROMPT: 'override' },
    secrets: { git_token: 'gt' },
    secret_env: { S: 'x' },
  })
  expect(b).toEqual({
    image: 'sandboxd-coding-agent:latest',
    env: { REPO: 'https://x/r.git', PROMPT: 'override', EXTRA: '1' },
    secret_env: { GIT_TOKEN: 'gt', S: 'x' },
  })
  const j = buildCreate(deps(), { preset: 'jupyter' })
  expect([j.image, j.cmd, j.idle_timeout_s]).toEqual([
    'sandboxd-jupyter:latest',
    undefined,
    4 * 3600,
  ])
  const c = buildCreate(deps(), {
    preset: 'jupyter',
    image: 'mine',
    cmd: ['sh'],
    idle_timeout_s: 120,
  })
  expect([c.image, c.cmd, c.idle_timeout_s]).toEqual(['mine', ['sh'], 120])
})

test('custom: image from the caller, else the configured default, else a 400', () => {
  expect(buildCreate(deps('dflt:1'), {}).image).toBe('dflt:1')
  expect(buildCreate(deps(), { image: 'python:3.12' }).image).toBe('python:3.12')
  expect(() => buildCreate(deps(), {})).toThrow(/implies no image/)
  expect(() => buildCreate(deps(), 'nope')).toThrow(/JSON object/)
  expect(() => buildCreate(deps(), { preset: 'nope' })).toThrow(/preset must be one of/)
  expect(() => buildCreate(deps(), { image: 'i', env: { A: 1 } })).toThrow(
    /env.A must be a string/,
  )
  // Unknown keys are not the UI's to judge: the API's strict schema names them.
  expect(buildCreate(deps(), { image: 'i', services: ['pg'] })).toEqual({
    image: 'i',
    env: {},
    secret_env: {},
  })
})
