import { loadConfig } from './config'
import { PresetRegistry, loadPresetDir, type LoadedPresets } from './presets/index'
import { createApp } from './app'
import { Log } from '@sandboxd/core/log'

const log = Log.create('ui')
const cfg = loadConfig()

// Presets are data on disk; a bad file is a boot error, not a runtime surprise.
let loaded: LoadedPresets
try {
  loaded = loadPresetDir(cfg.presetsDir)
} catch (e) {
  log.error('presets failed to load', { err: (e as Error).message })
  process.exit(1)
}
const presets = new PresetRegistry(loaded.presets)

const app = createApp({
  cfg,
  presets,
  // Re-read on every request so edits show up on reload without a restart.
  html: () => Bun.file(new URL('./index.html', import.meta.url)).text(),
})

const server = Bun.serve({ port: cfg.port, fetch: app.fetch })
log.info('listening', { url: server.url.toString(), api: cfg.apiUrl })
log.info('presets', { dir: loaded.dir, presets: presets.names })
