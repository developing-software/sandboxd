import { expect, test } from 'bun:test'
import { existsSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { loadConfig } from '../src/config'
import {
  PresetRegistry,
  loadPresetDir,
  parsePresetDoc,
  presetFromDoc,
  type Preset,
} from '../src/presets/index'

const loaded = loadPresetDir(resolve(import.meta.dir, '../presets'))
const reg = new PresetRegistry(loaded.presets)
const preset = (name: string): Preset => reg.get(name)!

// This app builds nothing: an image either comes from images/ (published, ships its own
// entry) or from somewhere else entirely. `ours` names the folder, or undefined.
const IMAGES = resolve(import.meta.dir, '../../../images')
const REGISTRY = 'ghcr.io/developing-software'
const ours = (image: string | null) =>
  image?.startsWith(`${REGISTRY}/sandboxd-`)
    ? image.slice(`${REGISTRY}/sandboxd-`.length).split(':')[0]
    : undefined

test('config: presets dir defaults to this app, image default is opt-in', () => {
  const cfg = loadConfig({ SANDBOXD_SERVICE_TOKEN: 't' })
  expect(cfg.presetsDir).toBe(resolve(import.meta.dir, '../presets'))
  expect(cfg.defaultImage).toBeNull()
  expect(cfg.apiUrl).toBe('http://localhost:8080')
  expect(
    loadConfig({
      SANDBOXD_PRESETS_DIR: '/etc/sandboxd/presets',
      SANDBOXD_API_URL: 'http://cp/',
    }),
  ).toMatchObject({ presetsDir: '/etc/sandboxd/presets', apiUrl: 'http://cp' })
})

test('shipped presets: one folder each; a built image ships its entry, a stock one a cmd', () => {
  expect(reg.names).toEqual([
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
  expect(reg.list().map((p) => [p.name, p.image, p.cmd?.[0] ?? null])).toEqual([
    ['agent', 'ghcr.io/developing-software/sandboxd-agent:latest', null],
    ['custom', null, null],
    ['http', 'python:3.12-slim', 'python3'],
    ['jupyter', 'ghcr.io/developing-software/sandboxd-jupyter:latest', null],
    ['node', 'node:22-slim', 'node'],
    ['notebook', 'quay.io/jupyter/minimal-notebook:latest', 'jupyter'],
    ['python', 'python:3.12-slim', 'python3'],
    ['ubuntu', 'ubuntu:24.04', 'bash'],
    ['vscode', 'ghcr.io/developing-software/sandboxd-vscode:latest', null],
  ])
  // One of ours ships its entry at /usr/local/bin/sandboxd-entry, so it implies no cmd;
  // a stock image has no entry of ours, so the preset must say what runs in the PTY.
  for (const p of reg.list())
    expect(p.cmd === null).toBe(p.image === null || ours(p.image) !== undefined)
})

test('presets over ours name an image this repo publishes, on the tag it publishes', () => {
  for (const p of reg.list()) {
    const name = ours(p.image)
    if (name === undefined) continue
    expect(p.image).toBe(`${REGISTRY}/sandboxd-${name}:latest`)
    expect(existsSync(resolve(IMAGES, name, 'Dockerfile'))).toBe(true)
  }
})

// The image discovers its own agents from that folder; this preset repeats the list so a UI
// can offer a menu and a typo is a 400. Nothing else may know an agent's name — see
// images/agent/agents/README.md.
test('agent preset: the enum mirrors images/agent/agents/, file for file', () => {
  const files = readdirSync(resolve(IMAGES, 'agent/agents'))
    .filter((f) => f.endsWith('.sh') && !f.startsWith('_'))
    .map((f) => f.replace(/\.sh$/, ''))
    .toSorted()
  const field = preset('agent').info.fields.find((f) => f.name === 'agent')!
  expect(field.values!.toSorted()).toEqual(files)
})

test('agent: fields expand to entry.sh env; secrets split out; absent fields emit nothing', () => {
  const x = preset('agent').expand({
    repo: 'https://x/r.git',
    prompt: 'do it',
    setup: ' bun install ',
    llm: { base_url: 'http://mine/', api_key: 'sk' },
    secrets: { git_token: 'gt' },
  })
  expect(x.env).toEqual({
    REPO: 'https://x/r.git',
    PROMPT: 'do it',
    SETUP: 'bun install',
    LLM_BASE_URL: 'http://mine',
  })
  expect(x.secret_env).toEqual({ LLM_API_KEY: 'sk', GIT_TOKEN: 'gt' })
  expect(x.image).toBe('ghcr.io/developing-software/sandboxd-agent:latest')
  expect(x.cmd).toBeUndefined()
  // AGENT / MODEL / BRANCH are defaulted by entry.sh, so an operator's SANDBOXD_SANDBOX_ENV_* survives (the API's
  // scheduler drops operator keys that the session env also sets).
  expect(preset('agent').expand({ repo: 'r' }).env).toEqual({ REPO: 'r' })
  expect(
    preset('agent').expand({ repo: 'r', agent: 'codex', model: 'gpt-5' }).env,
  ).toMatchObject({ AGENT: 'codex', MODEL: 'gpt-5' })
})

test('agent: validation messages', () => {
  const c = preset('agent')
  expect(() => c.expand({ prompt: 'p' })).toThrow(/repo is required/)
  expect(() => c.expand({ repo: '' })).toThrow(/repo is required/)
  expect(() => c.expand({ repo: 'r', agent: 'vim' })).toThrow(
    /agent must be one of claude, codex, opencode, shell/,
  )
  expect(() => c.expand({ repo: 'r', llm: { base_url: 'ftp://x' } })).toThrow(
    /llm.base_url must be http/,
  )
  expect(() => c.expand({ repo: 'r', llm: 'x' })).toThrow(/llm must be an object/)
  expect(() => c.expand({ repo: 'r', setup: 1 })).toThrow(/setup must be a string/)
})

test('jupyter: defaults for ui/port/idle, preview port from the port field, validation', () => {
  const j = preset('jupyter')
  const x = j.expand({})
  expect(x.env).toEqual({ JUPYTER_UI: 'lab', JUPYTER_PORT: '8888' })
  expect(x.secret_env).toEqual({})
  expect(x.image).toBe('ghcr.io/developing-software/sandboxd-jupyter:latest')
  expect(x.idle_timeout_s).toBe(4 * 3600)
  expect(j.info.preview).toEqual({ port: 8888, port_field: 'port' })
  const y = j.expand({
    repo: 'https://x/nb.git',
    ui: 'notebook',
    port: 9999,
    secrets: { git_token: 'gt' },
  })
  expect(y.env).toEqual({
    JUPYTER_UI: 'notebook',
    JUPYTER_PORT: '9999',
    REPO: 'https://x/nb.git',
  })
  expect(y.secret_env).toEqual({ GIT_TOKEN: 'gt' })
  expect(() => j.expand({ ui: 'vscode' })).toThrow(/ui must be one of lab, notebook/)
  expect(() => j.expand({ port: 70000 })).toThrow(/port must be an integer in 1..65535/)
  expect(() => j.expand({ port: '8888' })).toThrow(/port must be an integer/)
  expect(() => j.expand({ repo: 3 })).toThrow(/repo must be a string/)
})

test('vscode: editor only, previewed on the port field', () => {
  const v = preset('vscode')
  expect(v.expand({})).toEqual({
    env: { VSCODE_PORT: '8080' },
    secret_env: {},
    image: 'ghcr.io/developing-software/sandboxd-vscode:latest',
    idle_timeout_s: 14400,
  })
  expect(v.info.preview).toEqual({ port: 8080, port_field: 'port' })
  expect(v.expand({ repo: 'https://x/r.git', port: 3000 }).env).toEqual({
    REPO: 'https://x/r.git',
    VSCODE_PORT: '3000',
  })
})

test('custom: nothing implied, no image', () => {
  expect(preset('custom').expand({ anything: 1 })).toEqual({ env: {}, secret_env: {} })
  expect(preset('custom').info.fields).toEqual([])
})

test('registry: explicit name, claim by repo, fallback to custom, unknown -> 400, duplicates refused', () => {
  expect(reg.resolve({ preset: 'jupyter', repo: 'r' }).name).toBe('jupyter')
  expect(reg.resolve({ repo: 'r' }).name).toBe('agent')
  expect(reg.resolve({ repo: ' ' }).name).toBe('custom')
  expect(reg.resolve({ image: 'python:3.12' }).name).toBe('custom')
  expect(() => reg.resolve({ preset: 'nope' })).toThrow(
    /preset must be one of agent, custom, http, jupyter, node, notebook, python, ubuntu, vscode/,
  )
  expect(() => new PresetRegistry([preset('custom')], 'missing')).toThrow(/not registered/)
  expect(() => new PresetRegistry([preset('custom'), preset('custom')])).toThrow(
    /registered twice/,
  )
})

// ---- schema ----
const doc = (extra: Record<string, unknown> = {}) => ({
  description: 'd',
  image: 'i:1',
  ...extra,
})
const bad =
  (d: Record<string, unknown>, name = 'x') =>
  () =>
    parsePresetDoc(name, d)

test('schema: image/cmd/idle/preview/claims and their errors', () => {
  const p = parsePresetDoc('x', doc())
  expect([p.name, p.image, p.cmd, p.idle_timeout_s, p.preview, p.claims, p.fields]).toEqual([
    'x',
    'i:1',
    null,
    null,
    null,
    [],
    [],
  ])
  // No preset builds an image, so none is derived from the folder name: say which, or null.
  expect(bad({ description: 'd' })).toThrow(/image is required/)
  expect(parsePresetDoc('x', doc({ image: null })).image).toBeNull()
  expect(parsePresetDoc('x', doc({ image: 'ghcr.io/o/i:1 ' })).image).toBe('ghcr.io/o/i:1')
  expect(parsePresetDoc('x', doc({ cmd: ['sh'], idle_timeout_s: 60 }))).toMatchObject({
    cmd: ['sh'],
    idle_timeout_s: 60,
  })
  expect(parsePresetDoc('x', doc({ preview: { port: 3000 } })).preview).toEqual({
    port: 3000,
    port_field: null,
  })
  expect(bad({ image: 'i:1' })).toThrow(/description is required/)
  expect(bad(doc({ name: 'y' }))).toThrow(/does not match the directory/)
  expect(bad(doc({ bogus: 1 }))).toThrow(/unknown key "bogus"/)
  expect(bad(doc({ services: ['postgres'] }))).toThrow(/unknown key "services"/)
  expect(bad(doc({ image: '' }))).toThrow(/image is required/)
  expect(bad(doc({ cmd: [] }))).toThrow(/cmd must be/)
  expect(bad(doc({ idle_timeout_s: 5 }))).toThrow(/idle_timeout_s must be/)
  expect(bad(doc({ preview: { port: 0 } }))).toThrow(/preview.port must be/)
  expect(bad(doc({ preview: { port_field: 'port' } }))).toThrow(/is not a field/)
  expect(
    bad(doc({ preview: { port_field: 'p' }, fields: { p: { type: 'string' } } })),
  ).toThrow(/must be an int field/)
  expect(bad(doc({ preview: {} }))).toThrow(/give port or port_field/)
  expect(bad(doc({ claims_when: { present: 'repo' } }))).toThrow(/is not a field/)
  expect(bad(doc({ claims_when: { present: [] } }))).toThrow(/claims_when.present must be/)
  expect(
    parsePresetDoc(
      'x',
      doc({
        claims_when: { present: ['a', 'b'] },
        fields: { a: { type: 'string' }, b: { type: 'int' } },
      }),
    ).claims,
  ).toEqual(['a', 'b'])
})

test('schema: fields — env derivation, types, defaults, reserved and duplicate env', () => {
  const p = parsePresetDoc(
    'x',
    doc({
      fields: {
        'llm.base_url': { type: 'url' },
        token: { type: 'string', secret: true, env: 'GIT_TOKEN' },
        port: { type: 'int', min: 1, max: 10, default: 8 },
        ui: { type: 'enum', values: ['a', 'b'], default: 'a' },
        debug: { type: 'bool', default: true, description: 'd' },
        note: { type: 'string', multiline: true },
      },
    }),
  )
  expect(p.fields.map((f) => [f.name, f.env, f.type, f.secret, f.default])).toEqual([
    ['llm.base_url', 'LLM_BASE_URL', 'url', false, undefined],
    ['token', 'GIT_TOKEN', 'string', true, undefined],
    ['port', 'PORT', 'int', false, 8],
    ['ui', 'UI', 'enum', false, 'a'],
    ['debug', 'DEBUG', 'bool', false, true],
    ['note', 'NOTE', 'string', false, undefined],
  ])
  const field = (spec: Record<string, unknown>, name = 'f') =>
    bad(doc({ fields: { [name]: spec } }))
  expect(field({ type: 'date' })).toThrow(/type must be one of/)
  expect(field({ type: 'string', bogus: 1 })).toThrow(/unknown key "bogus"/)
  expect(field({ type: 'string' }, 'Bad-Name')).toThrow(/lowercase identifiers/)
  expect(field({ type: 'string', env: '1X' })).toThrow(/environment variable name/)
  expect(field({ type: 'string', env: 'TERM' })).toThrow(/is reserved/)
  expect(
    bad(doc({ fields: { a: { type: 'string', env: 'X' }, b: { type: 'string', env: 'X' } } })),
  ).toThrow(/already used by fields.a/)
  expect(field({ type: 'enum' })).toThrow(/values must be/)
  expect(field({ type: 'string', values: ['a'] })).toThrow(/only applies to enum/)
  expect(field({ type: 'string', min: 1 })).toThrow(/only apply to int/)
  expect(field({ type: 'int', min: 5, max: 1 })).toThrow(/min > max/)
  expect(field({ type: 'int', min: 1, max: 3, default: 9 })).toThrow(
    /default must be an integer in 1..3/,
  )
  expect(field({ type: 'enum', values: ['a'], default: 'z' })).toThrow(
    /default must be one of a/,
  )
  expect(field({ type: 'url', default: 'nope' })).toThrow(/default must be an http/)
  expect(field({ type: 'bool', default: 'yes' })).toThrow(/default must be a boolean/)
  expect(field({ type: 'string', required: true, default: 'x' })).toThrow(
    /cannot have a default/,
  )
  expect(field({ type: 'int', multiline: true })).toThrow(/multiline only applies/)
  expect(field({ type: 'string', required: 'yes' })).toThrow(/must be true or false/)
})

test('preset from doc: bool/int coercion, nested paths, url normalisation', () => {
  const p = presetFromDoc(
    parsePresetDoc(
      'x',
      doc({
        fields: {
          'a.b': { type: 'int', min: 1 },
          flag: { type: 'bool' },
          url: { type: 'url' },
          s: { type: 'string', default: 'dflt' },
        },
      }),
    ),
  )
  expect(p.expand({ a: { b: 3 }, flag: false, url: 'https://h/x//' }).env).toEqual({
    A_B: '3',
    FLAG: 'false',
    URL: 'https://h/x',
    S: 'dflt',
  })
  expect(() => p.expand({ a: { b: 0 } })).toThrow(/a.b must be an integer in 1../)
  expect(() => p.expand({ flag: 'no' })).toThrow(/flag must be true or false/)
  expect(() => p.expand({ a: [] })).toThrow(/a must be an object/)
  expect(p.claims).toBeUndefined()
})

test('loader: missing dir and empty dir are boot errors naming the path', () => {
  expect(() => loadPresetDir('/nonexistent/presets')).toThrow(
    /presets dir not found: \/nonexistent\/presets \(set SANDBOXD_PRESETS_DIR\)/,
  )
  expect(() => loadPresetDir(resolve(import.meta.dir, '../src/presets'))).toThrow(
    /no presets found in .*src\/presets/,
  )
})
