import { loadConfig } from './config'
import { DockerDriver } from './docker'
import { SessionManager } from './sessions'
import { Tunnel } from './tunnel'
import { Log } from '@sandboxd/core/log'

const log = Log.create('worker')
const cfg = loadConfig()
const driver = new DockerDriver(cfg.dockerSock, cfg.fingerprint)

// v1 limitation: sandboxes from a previous daemon run can't be re-attached
// (the PTY and ring buffer died with the process), so they are removed.
const orphans = await driver.listManaged()
for (const c of orphans.containers) {
  log.warn('removing orphaned container from previous run', { sid: c.sid })
  await driver.destroy(c.id)
}
for (const n of orphans.networks) {
  log.warn('removing orphaned network from previous run', { sid: n.sid })
  await driver.removeNetwork(n.sid)
}

const sessions = new SessionManager(driver, cfg.entry)
const tunnel = new Tunnel(cfg, sessions, driver)
sessions.on({
  started: (sid) => tunnel.send({ type: 'session.started', sid }),
  ended: (sid, reason, detail) => {
    tunnel.closePtyStreams(sid)
    tunnel.send({ type: 'session.ended', sid, reason, detail })
  },
})

log.info('starting', {
  name: cfg.name,
  cp: cfg.cpUrl,
  max_sessions: cfg.maxSessions,
  fingerprint: cfg.fingerprint.slice(0, 12),
})
tunnel.start()

const shutdown = async () => {
  log.info('shutting down')
  tunnel.stop()
  await sessions.endAll('lost')
  process.exit(0)
}
process.on('SIGINT', shutdown)
process.on('SIGTERM', shutdown)
