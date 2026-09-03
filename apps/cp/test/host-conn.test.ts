import { expect, test } from 'bun:test'
import { HostConn, type Transport } from '../src/hosts/conn'
import { decodeFrame } from '@sandboxd/core/framing'

class FakeTransport implements Transport {
  sent: (string | Uint8Array)[] = []
  closed = false
  send(d: string | Uint8Array) {
    this.sent.push(d)
  }
  close() {
    this.closed = true
  }
  /** JSON control messages sent so far. */
  get msgs() {
    return this.sent
      .filter((m): m is string => typeof m === 'string')
      .map((m) => JSON.parse(m))
  }
  get frames() {
    return this.sent.filter((m): m is Uint8Array => typeof m !== 'string').map(decodeFrame)
  }
}

const setup = () => {
  const t = new FakeTransport()
  return { t, c: new HostConn('h1', t, 0, 4) }
}

test('pty: odd stream ids, frames tagged with the stream, replay/close routed by id', () => {
  const { t, c } = setup()
  const got: string[] = []
  let replayed = 0,
    closed = 0
  const a = c.openPty(
    's_1',
    { cols: 80, rows: 24 },
    {
      onData: (d) => got.push(new TextDecoder().decode(d)),
      onReplay: () => replayed++,
      onClose: () => closed++,
    },
  )
  const b = c.openPty('s_2', { cols: 80, rows: 24 }, { onData: () => {}, onClose: () => {} })
  expect(t.msgs.map((m) => m.stream)).toEqual([1, 3])
  a.write(new TextEncoder().encode('ls\n'))
  expect(t.frames[0]!.stream).toBe(1)
  c.onFrame(1, new TextEncoder().encode('out'))
  c.onFrame(3, new TextEncoder().encode('other'))
  expect(got).toEqual(['out'])
  c.onStreamMsg({ type: 'pty.replay', stream: 1 })
  expect(replayed).toBe(1)
  a.resize({ cols: 100, rows: 30 })
  expect(t.msgs.at(-1)).toEqual({
    type: 'pty.resize',
    sid: 's_1',
    size: { cols: 100, rows: 30 },
  })
  c.onStreamMsg({ type: 'pty.closed', stream: 1 })
  expect(closed).toBe(1)
  a.close() // already gone: no second pty.close
  expect(t.msgs.filter((m) => m.type === 'pty.close')).toHaveLength(0)
  b.close()
  expect(t.msgs.at(-1)).toEqual({ type: 'pty.close', stream: 3 })
})

test('port: dial resolves on port.open and rejects on port.error; closeAll notifies everyone', async () => {
  const { t, c } = setup()
  let closedA = 0,
    closedB = 0
  const pa = c.dialPort('s_1', 3000, { onData: () => {}, onClose: () => closedA++ })
  const pb = c.dialPort('s_1', 3001, { onData: () => {}, onClose: () => closedB++ })
  expect(t.msgs.map((m) => [m.type, m.port, m.stream])).toEqual([
    ['port.dial', 3000, 1],
    ['port.dial', 3001, 3],
  ])
  c.onStreamMsg({ type: 'port.open', stream: 1 })
  const a = await pa
  a.write(new Uint8Array([7]))
  expect(t.frames.at(-1)).toEqual({ stream: 1, payload: new Uint8Array([7]) })
  c.onStreamMsg({ type: 'port.error', stream: 3, msg: 'connection refused' })
  await expect(pb).rejects.toThrow('connection refused')
  expect(closedB).toBe(1)
  c.closeAll()
  expect(closedA).toBe(1)
  expect(closedB).toBe(1) // not notified twice
})

test('createSession counts optimistically until the next heartbeat', () => {
  const { t, c } = setup()
  c.createSession({
    sid: 's',
    image: 'i',
    cmd: null,
    idle_timeout_s: 60,
    env: {},
    secret_env: {},
    services: [],
  })
  expect(c.capacity()).toEqual({ running: 1, max: 4 })
  expect(t.msgs[0]!.type).toBe('session.create')
})
