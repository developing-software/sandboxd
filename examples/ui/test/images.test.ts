// Where this app meets `images/`. It builds nothing and imports nothing from there — the
// two meet at a tag and a set of variable names, exactly as an external app would — so
// these are the only assertions that reach across, and they exist because a preset naming
// a tag nobody publishes is a sandbox that ends `failed` with a pull error.
import { expect, test } from 'bun:test'
import { existsSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { PresetRegistry, loadPresetDir } from '../src/presets/index'

const reg = new PresetRegistry(loadPresetDir(resolve(import.meta.dir, '../presets')).presets)
const IMAGES = resolve(import.meta.dir, '../../../images')
const REGISTRY = 'ghcr.io/developing-software'

/** The `images/` folder behind a preset's image, or undefined for a stock one. */
const ours = (image: string | null) =>
  image?.startsWith(`${REGISTRY}/sandboxd-`)
    ? image.slice(`${REGISTRY}/sandboxd-`.length).split(':')[0]
    : undefined

test('a preset over one of ours names a folder that exists, on the tag CI publishes', () => {
  const seen: string[] = []
  for (const p of reg.list()) {
    const name = ours(p.image)
    if (name === undefined) continue
    expect(p.image).toBe(`${REGISTRY}/sandboxd-${name}:latest`)
    expect(existsSync(resolve(IMAGES, name, 'Dockerfile'))).toBe(true)
    seen.push(name)
  }
  expect(seen.toSorted()).toEqual(['agent', 'jupyter', 'vscode'])
})

test('ours ship an entry so they imply no cmd; a stock image needs one', () => {
  // One of ours installs /usr/local/bin/sandboxd-entry, which the worker execs when the
  // sandbox has no cmd. A stock image has no entry of ours, so the preset must say what
  // runs in the PTY, or the PTY gets the image's own default and the preset means nothing.
  for (const p of reg.list())
    expect(p.cmd === null).toBe(p.image === null || ours(p.image) !== undefined)
})

test('the agent enum mirrors images/agent/agents/, file for file', () => {
  // The image discovers its own agents from that folder; the preset repeats the list only
  // so a UI has a menu and a typo is a 400 rather than a silent shell. Nothing else may
  // know an agent's name — see images/agent/agents/README.md.
  const files = readdirSync(resolve(IMAGES, 'agent/agents'))
    .filter((f) => f.endsWith('.sh') && !f.startsWith('_'))
    .map((f) => f.replace(/\.sh$/, ''))
    .toSorted()
  const field = reg.get('agent')!.info.fields.find((f) => f.name === 'agent')!
  expect(field.values!.toSorted()).toEqual(files)
})
