import { expect, test } from 'bun:test'
import { SessionManager } from '../src/sessions'
import type { CreateOpts, Duplex, PtyStream, SandboxDriver } from '../src/driver'
import type { EndReason, SessionSpec } from '@sandboxd/core/messages'

/** Records every call; `readyAfter` dial attempts fail before a service port accepts. */
class FakeDriver implements SandboxDriver {
  log: string[] = []
  created: CreateOpts[] = []
  readyAfter = 0
  dials = 0
  failCreateOf: string | null = null
  private n = 0

  async create(o: CreateOpts) {
    if (o.image === this.failCreateOf) throw new Error(`pull failed: ${o.image}`)
    this.created.push(o)
    const id = `c${++this.n}:${o.alias ?? o.role}`
    this.log.push(`create ${id}`)
    return id
  }
  async attach(id: string, cmd: string[], env: Record<string, string>): Promise<PtyStream> {
    this.log.push(`attach ${id} ${cmd.join(' ')} ${Object.keys(env).toSorted().join(',')}`)
    return { write() {}, async resize() {}, close() {}, onData() {}, onExit() {} }
  }
  async dial(id: string, port: number): Promise<Duplex> {
    this.dials++
    if (this.dials <= this.readyAfter) throw new Error('connection refused')
    this.log.push(`dial ${id}:${port}`)
    return { write() {}, end() {}, onData() {}, onClose() {} }
  }
  async destroy(id: string) {
    this.log.push(`destroy ${id}`)
  }
  async createNetwork(sid: string) {
    this.log.push(`net+ ${sid}`)
    return `sandboxd-${sid}`
  }
  async removeNetwork(sid: string) {
    this.log.push(`net- ${sid}`)
  }
  async listManaged() {
    return { containers: [], networks: [] }
  }
}

const spec = (services: SessionSpec['services']): SessionSpec => ({
  sid: 's_1',
  image: 'sandbox:1',
  cmd: null,
  idle_timeout_s: 60,
  env: { A: '1' },
  secret_env: { S: 'x' },
  services,
})
const services: SessionSpec['services'] = [
  {
    name: 'db',
    image: 'postgres:16',
    env: { POSTGRES_USER: 'app' },
    secret_env: { POSTGRES_PASSWORD: 'pw' },
    cmd: null,
    ready: { port: 5432, timeout_s: 5 },
  },
  { name: 'cache', image: 'redis:7', env: {}, secret_env: {}, cmd: null, ready: null },
]

function setup() {
  const driver = new FakeDriver()
  const mgr = new SessionManager(driver, ['/entry'], async () => {})
  const events: string[] = []
  mgr.on({
    started: (sid) => events.push(`started ${sid}`),
    ended: (sid, r: EndReason, d) => events.push(`ended ${sid} ${r}${d ? ' ' + d : ''}`),
  })
  return { driver, mgr, events }
}

test('pod: network, services in order with readiness, then the sandbox; teardown in reverse', async () => {
  const { driver, mgr, events } = setup()
  driver.readyAfter = 2
  const s = spec(services)
  await mgr.create(s)
  expect(driver.log).toEqual([
    'net+ s_1',
    'create c1:db',
    'dial c1:db:5432',
    'create c2:cache',
    'create c3:sandbox',
    'attach c3:sandbox /entry A,S,SANDBOXD_SESSION_ID,TERM',
  ])
  expect(driver.dials).toBe(3)
  const db = driver.created[0]!
  expect([db.role, db.network, db.alias, db.env]).toEqual([
    'service',
    'sandboxd-s_1',
    'db',
    { POSTGRES_USER: 'app', POSTGRES_PASSWORD: 'pw' },
  ])
  expect(driver.created[2]).toEqual({
    sid: 's_1',
    image: 'sandbox:1',
    role: 'sandbox',
    network: 'sandboxd-s_1',
    alias: 'sandbox',
  })
  expect(s.secret_env).toEqual({})
  expect(s.services[0]!.secret_env).toEqual({}) // dropped after hand-off
  expect(events).toEqual(['started s_1'])
  expect(mgr.containerOf('s_1')).toBe('c3:sandbox')

  driver.log = []
  await mgr.end('s_1', 'closed')
  expect(driver.log).toEqual([
    'destroy c3:sandbox',
    'destroy c2:cache',
    'destroy c1:db',
    'net- s_1',
  ])
  expect(events.at(-1)).toBe('ended s_1 closed')
})

test('no services: no network, sandbox on the default bridge', async () => {
  const { driver, mgr } = setup()
  await mgr.create(spec([]))
  expect(driver.log[0]).toBe('create c1:sandbox')
  expect(driver.created[0]).toEqual({ sid: 's_1', image: 'sandbox:1', role: 'sandbox' })
})

test('readiness timeout fails the session and removes what was started', async () => {
  const { driver, mgr, events } = setup()
  driver.readyAfter = Infinity
  const fast = { ...services[0]!, ready: { port: 5432, timeout_s: 0 } }
  await mgr.create(spec([fast]))
  expect(events[0]).toMatch(/^ended s_1 failed .*service db not ready on port 5432/)
  expect(driver.log).toEqual(['net+ s_1', 'create c1:db', 'destroy c1:db', 'net- s_1'])
  expect(mgr.count()).toBe(0)
})

test('a failing service image fails the session; earlier services and the network are cleaned up', async () => {
  const { driver, mgr, events } = setup()
  driver.failCreateOf = 'redis:7'
  await mgr.create(spec(services))
  expect(events[0]).toMatch(/^ended s_1 failed .*pull failed: redis:7/)
  expect(driver.log).toEqual([
    'net+ s_1',
    'create c1:db',
    'dial c1:db:5432',
    'destroy c1:db',
    'net- s_1',
  ])
})

test("a service's cmd (compose `command:`) reaches the driver; services without one get none", async () => {
  const { driver, mgr } = setup()
  await mgr.create(
    spec([{ ...services[1]!, cmd: ['redis-server', '--appendonly', 'yes'] }, services[0]!]),
  )
  expect(driver.created[0]!.cmd).toEqual(['redis-server', '--appendonly', 'yes'])
  expect('cmd' in driver.created[1]!).toBe(false)
})
