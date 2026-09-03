import { expect, test } from 'bun:test'
import { proxyOnce } from '../src/preview/http'

const req = () => new Request('http://3000-s_x.preview.localhost/', { method: 'GET' })

test('dial failure that also fires onClose -> 502, no unhandled rejection', async () => {
  let unhandled: unknown = null
  const h = (e: unknown) => {
    unhandled = e
  }
  process.on('unhandledRejection', h)
  try {
    const res = await proxyOnce(req(), 3000, async (sub) => {
      sub.onClose()
      throw new Error('connection refused')
    })
    expect(res.status).toBe(502)
    expect(await res.text()).toContain('could not reach port 3000')
    await new Promise((r) => setTimeout(r, 20))
    expect(unhandled).toBeNull()
  } finally {
    process.off('unhandledRejection', h)
  }
})

test('upstream closes before headers -> 502', async () => {
  const res = await proxyOnce(req(), 3000, async (sub) => {
    queueMicrotask(() => sub.onClose())
    return { write() {}, close() {} }
  })
  expect(res.status).toBe(502)
  expect(await res.text()).toContain('upstream closed before response headers')
})

test('normal response is streamed through', async () => {
  const res = await proxyOnce(req(), 3000, async (sub) => ({
    write() {
      queueMicrotask(() => {
        sub.onData(
          new TextEncoder().encode(
            'HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\ncontent-length: 2\r\n\r\nhi',
          ),
        )
      })
    },
    close() {},
  }))
  expect(res.status).toBe(200)
  expect(await res.text()).toBe('hi')
})
