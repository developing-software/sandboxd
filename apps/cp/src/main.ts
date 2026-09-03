import type { ServerWebSocket } from 'bun'
import { loadConfig } from './config'
import { Store } from './store'
import { Tokens } from './tokens'
import { HostHub, type TunnelData } from './hosts/hub'
import { HostService } from './hosts/service'
import { Scheduler } from './scheduler'
import { AttachBridge, type AttachData } from './attach'
import { PreviewProxy, type PreviewWsData } from './preview/proxy'
import { PresetRegistry, loadPresetDir, type LoadedPresets } from './presets/index'
import { SessionService } from './sessions'
import { Api } from './http'
import { logger } from '@sandboxd/core/log'

const log = logger('cp')
// A stray rejection in a proxy or tunnel callback must never take the control plane down.
process.on('unhandledRejection', (e) => log.error('unhandled rejection', { err: String(e) }))
process.on('uncaughtException', (e) =>
  log.error('uncaught exception', { err: String(e), stack: e?.stack }),
)

const cfg = loadConfig()
// Presets and the service catalog are data on disk; a bad file is a boot error, not a runtime surprise.
let loaded: LoadedPresets
try {
  loaded = loadPresetDir(cfg.presetsDir)
} catch (e) {
  log.error('presets failed to load', { err: (e as Error).message })
  process.exit(1)
}
const presets = new PresetRegistry(loaded.presets)
const store = new Store(cfg.dbPath)
const tokens = new Tokens(cfg.secret)

const hub = new HostHub(store)
const sched = new Scheduler(store, hub, cfg.sandboxEnv)
hub.on(sched)
sched.boot()

const attach = new AttachBridge(store, hub)
const preview = new PreviewProxy(store, hub, tokens, cfg.previewDomain)
const sessions = new SessionService(cfg, store, hub, sched, tokens, presets, loaded.catalog)
const api = new Api(
  cfg,
  tokens,
  new HostService(store, hub),
  sessions,
  presets,
  loaded.catalog,
)

type Data = TunnelData | AttachData | PreviewWsData

const server = Bun.serve<Data>({
  port: cfg.port,
  idleTimeout: 255,
  async fetch(req, srv) {
    const pv = preview.match(req)
    if (pv) return preview.handle(req, pv, srv)

    const url = new URL(req.url)
    if (url.pathname === '/tunnel') {
      const data: TunnelData = { kind: 'tunnel', hostId: null }
      return srv.upgrade(req, { data })
        ? undefined
        : new Response('upgrade required', { status: 426 })
    }
    return api.handle(req, srv)
  },
  websocket: {
    maxPayloadLength: 16 * 1024 * 1024,
    open(ws: ServerWebSocket<Data>) {
      if (ws.data.kind === 'tunnel') hub.onOpen(ws as ServerWebSocket<TunnelData>)
      else if (ws.data.kind === 'preview')
        ws.data.bridge.attach(ws as ServerWebSocket<PreviewWsData>)
      else attach.onOpen(ws as ServerWebSocket<AttachData>)
    },
    message(ws: ServerWebSocket<Data>, msg) {
      if (ws.data.kind === 'tunnel') hub.onMessage(ws as ServerWebSocket<TunnelData>, msg)
      else if (ws.data.kind === 'preview') ws.data.bridge.onBrowserMessage(msg)
      else attach.onMessage(ws as ServerWebSocket<AttachData>, msg)
    },
    close(ws: ServerWebSocket<Data>, code, reason) {
      if (ws.data.kind === 'tunnel') hub.onClose(ws as ServerWebSocket<TunnelData>)
      else if (ws.data.kind === 'preview') ws.data.bridge.onBrowserClose(code, reason)
      else attach.onClose(ws as ServerWebSocket<AttachData>)
    },
  },
})

log.info('listening', {
  url: server.url.toString(),
  public: cfg.publicUrl,
  preview: `*.${cfg.previewDomain}`,
  dev: cfg.dev,
  db: cfg.dbPath,
})
log.info('presets', {
  dir: loaded.dir,
  presets: presets.names,
  services: loaded.catalog.names,
})
log.info('defaults', { image: cfg.defaultImage, sandbox_env: Object.keys(cfg.sandboxEnv) })
