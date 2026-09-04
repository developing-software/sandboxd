import { expect, test } from 'bun:test'
import { SessionManager } from '../src/sessions'
import type { CreateOpts, Duplex, PtyStream, SandboxDriver } from '../src/driver'
import type { Msg } from '@sandboxd/core/messages'

/** Records every call; `failCreateOf` makes one image fail to create. */
class FakeDriver implements SandboxDriver {
  log: string[] = []
  created: CreateOpts[] = []
  failCreateOf: string | null = null
  private n = 0

  async create(o: CreateOpts) {
    if (o.image === this.failCreateOf) throw new Error(`pull failed: ${o.image}`)
    this.created.push(o)
    const id = `c${++this.n}`
    this.log.push(`create ${id}`)
    return id
  }
  async attach(id: string, cmd: string[], env: Record<string, string>): Promise<PtyStream> {
    this.log.push(`attach ${id} ${cmd.join(' ')} ${Object.keys(env).toSorted().join(',')}`)
    return { write() {}, async resize() {}, close() {}, onData() {}, onExit() {} }
  }
  async dial(id: string, port: number): Promise<Duplex> {
    this.log.push(`dial ${id}:${port}`)
    return { write() {}, end() {}, onData() {}, onClose() {} }
  }
  async destroy(id: string) {
    this.log.push(`destroy ${id}`)
  }
  async listManaged() {
    return []
  }
}

const spec = (image = 'sandbox:1'): Msg.Spec => ({
  sid: 's_1',
  image,
  cmd: null,
  idle_timeout_s: 60,
  env: { A: '1' },
  secret_env: { S: 'x' },
})

function setup() {
  const driver = new FakeDriver()
  const mgr = new SessionManager(driver, ['/entry'])
  const events: string[] = []
  mgr.on({
    started: (sid) => events.push(`started ${sid}`),
    ended: (sid, r: Msg.EndReason, d) => events.push(`ended ${sid} ${r}${d ? ' ' + d : ''}`),
  })
  return { driver, mgr, events }
}

test('create: one container, the entry exec’d with env + TERM + session id; secrets dropped after hand-off', async () => {
  const { driver, mgr, events } = setup()
  const s = spec()
  await mgr.create(s)
  expect(driver.log).toEqual(['create c1', 'attach c1 /entry A,S,SANDBOXD_SESSION_ID,TERM'])
  expect(driver.created).toEqual([{ sid: 's_1', image: 'sandbox:1' }])
  expect(s.secret_env).toEqual({})
  expect(events).toEqual(['started s_1'])
  expect(mgr.containerOf('s_1')).toBe('c1')

  driver.log = []
  await mgr.end('s_1', 'closed')
  expect(driver.log).toEqual(['destroy c1'])
  expect(events.at(-1)).toBe('ended s_1 closed')
  expect(mgr.count()).toBe(0)
})

test('a failing image fails the session and creates nothing to clean up', async () => {
  const { driver, mgr, events } = setup()
  driver.failCreateOf = 'missing:1'
  await mgr.create(spec('missing:1'))
  expect(events).toEqual(['ended s_1 failed Error: pull failed: missing:1'])
  expect(driver.log).toEqual([])
  expect(mgr.count()).toBe(0)
})

test('cmd from the spec wins over the daemon entry; a duplicate create is ignored', async () => {
  const { driver, mgr } = setup()
  await mgr.create({ ...spec(), cmd: ['bash', '-l'] })
  await mgr.create(spec())
  expect(driver.log).toEqual(['create c1', 'attach c1 bash -l A,S,SANDBOXD_SESSION_ID,TERM'])
})
