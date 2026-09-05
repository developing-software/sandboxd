#!/usr/bin/env bun
// Builds one Docker image per preset: every presets/<name>/ with a Dockerfile,
// tagged with the image the preset declares (default sandboxd-<name>:latest),
// so what is built is exactly what the control plane will ask hosts to run.
//   bun run image              # all presets
//   bun run image vscode       # a subset
// Images are built locally; every agent host needs them (the daemon only pulls
// on a 404), so push them to a registry and set `image:` in preset.yaml for
// multi-host setups.
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { loadPresetDir } from '../src/presets/index'

const dir = process.env.SANDBOXD_PRESETS_DIR ?? import.meta.dir
const only = new Set(process.argv.slice(2))
const { presets } = loadPresetDir(dir)

const targets = presets.filter((p) => only.size === 0 || only.has(p.name))
for (const name of only)
  if (!presets.some((p) => p.name === name)) {
    console.error(`unknown preset "${name}"; have: ${presets.map((p) => p.name).join(', ')}`)
    process.exit(2)
  }

const built: string[] = []
for (const p of targets) {
  const ctx = join(dir, p.name)
  if (!existsSync(join(ctx, 'Dockerfile'))) {
    console.log(`▸ ${p.name}: no Dockerfile, nothing to build`)
    continue
  }
  if (!p.info.image) {
    console.log(`▸ ${p.name}: image: null, nothing to build`)
    continue
  }
  console.log(`▸ ${p.name}: docker build -t ${p.info.image} ${ctx}`)
  const r = Bun.spawnSync(['docker', 'build', '-t', p.info.image, ctx], {
    stdio: ['inherit', 'inherit', 'inherit'],
  })
  if (r.exitCode !== 0) {
    console.error(`✗ ${p.name}: docker build exited with ${r.exitCode}`)
    process.exit(r.exitCode || 1)
  }
  built.push(p.info.image)
}
console.log(built.length ? `✓ built ${built.join(', ')}` : 'nothing built')
