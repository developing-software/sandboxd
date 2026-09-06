import { createApi } from './api'
import { loadConfig } from './config'
import { Log } from './log'
import hosts from './pages/hosts.html'
import create from './pages/new.html'
import sandbox from './pages/sandbox.html'
import sandboxes from './pages/sandboxes.html'
import { PresetRegistry, loadPresetDir, type LoadedPresets } from './presets/index'

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
const api = createApi({ cfg, presets })

// A page is an html file and the TypeScript it references, bundled by Bun at startup
// (and rebuilt on edit under --hot). Everything that is not a page is /api, the Hono app.
const server = Bun.serve({
  port: cfg.port,
  routes: {
    '/': new Response(null, { status: 302, headers: { location: '/sandboxes' } }),
    '/hosts': hosts,
    '/sandboxes': sandboxes,
    '/sandboxes/new': create,
    '/sandboxes/:id': sandbox,
  },
  fetch: api.fetch,
})
log.info('listening', { url: server.url.toString(), api: cfg.apiUrl })
log.info('presets', { dir: loaded.dir, presets: presets.names })
