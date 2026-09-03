// End to end: browser WebSocket -> CP preview proxy -> (fake tunnel = TCP) -> upstream WebSocket server.
import { afterAll, beforeAll, expect, test } from 'bun:test'
import type { ServerWebSocket } from 'bun'
import { Store } from '../src/store'
import { Tokens } from '../src/tokens'
import { COOKIE, PreviewProxy, type PreviewWsData } from '../src/preview/proxy'
import type { PortDialer } from '../src/hosts/hub'

const SID = 's_abc234',
  HOST = 'h1'
let upstream: ReturnType<typeof Bun.serve>, cp: ReturnType<typeof Bun.serve>
let cookie: string
const seen: { path: string; headers: Record<string, string> }[] = []

class FakeHub implements PortDialer {
  isOnline(id: string) {
    return id === HOST
  }
  async dialPort(
    _h: string,
    _sid: string,
    port: number,
    sub: { onData(d: Uint8Array): void; onClose(): void },
  ) {
    const sock = await Bun.connect({
      hostname: '127.0.0.1',
      port,
      socket: {
        data(_s, d) {
          sub.onData(new Uint8Array(d))
        },
        close() {
          sub.onClose()
        },
        error() {
          sub.onClose()
        },
      },
    })
    return {
      write(d: Uint8Array) {
        sock.write(d)
      },
      close() {
        sock.end()
      },
    }
  }
}

beforeAll(() => {
  upstream = Bun.serve<{ proto: string | null }>({
    port: 0,
    async fetch(req, server) {
      const url = new URL(req.url)
      seen.push({ path: url.pathname, headers: Object.fromEntries(req.headers) })
      if (req.headers.get('upgrade') === 'websocket') {
        if (url.pathname === '/refuse') return new Response('no ws here', { status: 403 })
        const proto = req.headers.get('sec-websocket-protocol')?.split(',')[0]?.trim() ?? null
        const ok = server.upgrade(req, {
          data: { proto },
          ...(proto ? { headers: { 'sec-websocket-protocol': proto } } : {}),
        })
        return ok ? undefined : new Response('upgrade failed', { status: 500 })
      }
      if (req.method === 'POST')
        return new Response(`posted:${url.pathname}:` + (await req.text()))
      return new Response(`plain:${url.pathname}`)
    },
    websocket: {
      open(ws) {
        ws.send(`hello:${ws.data.proto ?? 'none'}`)
      },
      message(ws, m) {
        if (typeof m === 'string') {
          if (m === 'close-me') ws.close(4001, 'server says bye')
          else ws.send('echo:' + m)
        } else ws.send(m)
      },
    },
  })

  const store = new Store(':memory:')
  store.insertPendingHost({
    id: HOST,
    name: HOST,
    fingerprint: 'fp',
    approve_code: 'AAAA-AA',
    max_sessions: 1,
  })
  store.approveHost(HOST)
  store.insertSession({
    id: SID,
    owner_id: 'o',
    image: 'img',
    cmd: null,
    env: {},
    services: [],
    idle_timeout_s: 60,
    created_at: 1,
  })
  store.markCreating(SID, HOST)
  store.markRunning(SID)
  const tokens = new Tokens('secret')
  cookie = `${COOKIE}=${tokens.sign({ k: 'preview-cookie', sid: SID, port: upstream.port! }, 60_000)}`
  const proxy = new PreviewProxy(store, new FakeHub(), tokens, 'preview.localhost')

  cp = Bun.serve<PreviewWsData>({
    port: 0,
    async fetch(req, server) {
      const pv = proxy.match(req)
      if (!pv) return new Response('not preview', { status: 404 })
      return proxy.handle(req, pv, server)
    },
    websocket: {
      open(ws: ServerWebSocket<PreviewWsData>) {
        ws.data.bridge.attach(ws)
      },
      message(ws, m) {
        ws.data.bridge.onBrowserMessage(m)
      },
      close(ws, code, reason) {
        ws.data.bridge.onBrowserClose(code, reason)
      },
    },
  })
})
afterAll(() => {
  cp.stop(true)
  upstream.stop(true)
})

const previewHost = () => `${upstream.port}-${SID}.preview.localhost`
const headers = () => ({ host: previewHost(), cookie })

function connect(path: string, protocols?: string[]) {
  const ws = new WebSocket(`ws://127.0.0.1:${cp.port}${path}`, {
    headers: headers(),
    protocols,
  } as any)
  const events: (string | Uint8Array | { closed: [number, string] })[] = []
  const waiters: ((e: unknown) => void)[] = []
  const push = (e: (typeof events)[number]) => {
    const w = waiters.shift()
    if (w) w(e)
    else events.push(e)
  }
  ws.binaryType = 'arraybuffer'
  ws.onmessage = (ev) =>
    push(typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data as ArrayBuffer))
  ws.onclose = (ev) => push({ closed: [ev.code, ev.reason] })
  const next = () =>
    events.length
      ? Promise.resolve(events.shift()!)
      : new Promise<unknown>((r) => waiters.push(r))
  const opened = new Promise<void>((res, rej) => {
    ws.onopen = () => res()
    ws.onerror = (e) => rej(e)
  })
  return { ws, next, opened }
}

test('http path still proxies and forwards the original host', async () => {
  const r = await fetch(`http://127.0.0.1:${cp.port}/x?y=1`, { headers: headers() })
  expect(await r.text()).toBe('plain:/x')
  const p = await fetch(`http://127.0.0.1:${cp.port}/post`, {
    method: 'POST',
    headers: headers(),
    body: 'body!',
  })
  expect(await p.text()).toBe('posted:/post:body!')
  const last = seen.at(-1)!
  expect(last.headers['x-forwarded-host']).toBe(previewHost())
  expect(last.headers['cookie']).toBeUndefined()
})

test('websocket: text and binary echo both ways, subprotocol honoured', async () => {
  const c = connect('/api/kernels/k/channels', ['v1.kernel.websocket.jupyter.org'])
  await c.opened
  expect(c.ws.protocol).toBe('v1.kernel.websocket.jupyter.org')
  expect(await c.next()).toBe('hello:v1.kernel.websocket.jupyter.org')
  c.ws.send('ping')
  expect(await c.next()).toBe('echo:ping')
  const big = new Uint8Array(200_000).map((_, i) => i % 251)
  c.ws.send(big)
  expect(await c.next()).toEqual(big)
  const up = seen.at(-1)!
  expect(up.headers['sec-websocket-extensions']).toBeUndefined()
  expect(up.headers['x-forwarded-host']).toBe(previewHost())
  c.ws.close(1000, 'done')
  // Bun answers a client-initiated close itself and does not echo the reason text.
  expect(((await c.next()) as { closed: [number, string] }).closed[0]).toBe(1000)
})

test('websocket: upstream-initiated close reaches the browser with its code', async () => {
  const c = connect('/ws')
  await c.opened
  expect(await c.next()).toBe('hello:none')
  c.ws.send('close-me')
  expect(await c.next()).toEqual({ closed: [4001, 'server says bye'] })
})

test('websocket: upstream refusal is passed through as a plain response', async () => {
  const r = await fetch(`http://127.0.0.1:${cp.port}/refuse`, {
    headers: {
      ...headers(),
      upgrade: 'websocket',
      connection: 'Upgrade',
      'sec-websocket-key': 'x',
      'sec-websocket-version': '13',
    },
  })
  expect(r.status).toBe(403)
})

test('websocket: no cookie -> 401 before dialing', async () => {
  const before = seen.length
  const r = await fetch(`http://127.0.0.1:${cp.port}/ws`, {
    headers: { host: previewHost(), upgrade: 'websocket', connection: 'Upgrade' },
  })
  expect(r.status).toBe(401)
  expect(seen.length).toBe(before)
})
