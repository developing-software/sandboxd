// Where a page gets the port it offers to open. A preset's preview port is a field, and a
// field is an env var, so the sandbox the API hands back still names it — which is what
// makes the link work on a sandbox the create form did not just produce. The sandboxes
// below are built with buildCreate so the env under test is the env that would be sent.
import { expect, test } from 'bun:test'
import { resolve } from 'node:path'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'
import { Preview } from '../src/preview'
import { buildCreate } from '../src/sandboxes'

const registry = new PresetRegistry(
  loadPresetDir(resolve(import.meta.dir, '../presets')).presets,
)
const presets = registry.list()
const made = (body: Record<string, unknown>) => ({
  env: buildCreate({ defaultImage: null, presets: registry }, body).env ?? {},
})

test('a preset that names a port field is found again in the sandbox it created', () => {
  expect(Preview.detect(presets, made({ preset: 'vscode' }))).toEqual({
    port: 8080,
    preset: 'vscode',
  })
  expect(Preview.detect(presets, made({ preset: 'vscode', port: 3001 }))).toEqual({
    port: 3001,
    preset: 'vscode',
  })
  expect(Preview.detect(presets, made({ preset: 'jupyter', port: 9000 }))).toEqual({
    port: 9000,
    preset: 'jupyter',
  })
})

test('a sandbox that declared no port offers none, and the page keeps its port box', () => {
  // An agent's dev server is on whatever port the repo chose; nothing here could know it.
  expect(Preview.detect(presets, made({ repo: 'https://x/r.git' }))).toBeNull()
  // `http` and `notebook` put their port in cmd, not env: a literal `preview: {port}`
  // leaves no trace on the sandbox, so the list shows no link and the port is typed.
  expect(Preview.detect(presets, made({ preset: 'http' }))).toBeNull()
  expect(Preview.detect(presets, made({ preset: 'ubuntu' }))).toBeNull()
})

test('a port that is not one is not offered', () => {
  expect(Preview.detect(presets, { env: { VSCODE_PORT: '' } })).toBeNull()
  expect(Preview.detect(presets, { env: { VSCODE_PORT: 'eight thousand' } })).toBeNull()
  expect(Preview.detect(presets, { env: { VSCODE_PORT: '70000' } })).toBeNull()
})
