import { expect, test } from 'bun:test'
import { ResponseParser } from '../src/preview/response-parser'

const b = (s: string) => new TextEncoder().encode(s)
const s = (u: Uint8Array) => new TextDecoder().decode(u)

function collect(p: ResponseParser, chunks: string[]) {
  let head: any = null,
    body = '',
    ended = false
  for (const c of chunks)
    for (const ev of p.feed(b(c))) {
      if (ev.kind === 'head') head = ev
      else if (ev.kind === 'body') body += s(ev.data)
      else ended = true
    }
  return { head, body, ended }
}

test('content-length body across split chunks', () => {
  const r = collect(new ResponseParser(), [
    'HTTP/1.1 200 OK\r\ncontent-len',
    'gth: 5\r\nx-a: b\r\n\r\nhel',
    'lo',
  ])
  expect(r.head.status).toBe(200)
  expect(r.head.headers.get('x-a')).toBe('b')
  expect(r.body).toBe('hello')
  expect(r.ended).toBe(true)
})

test('chunked body, chunk boundaries split arbitrarily', () => {
  const raw =
    'HTTP/1.1 200 OK\r\ntransfer-encoding: chunked\r\n\r\n5\r\nhello\r\n6;ext=1\r\n world\r\n0\r\n\r\n'
  for (const n of [1, 3, 7, 100]) {
    const chunks: string[] = []
    for (let i = 0; i < raw.length; i += n) chunks.push(raw.slice(i, i + n))
    const r = collect(new ResponseParser(), chunks)
    expect(r.body).toBe('hello world')
    expect(r.ended).toBe(true)
  }
})

test('no framing: body until close', () => {
  const r = collect(new ResponseParser(), ['HTTP/1.1 200 OK\r\n\r\nabc', 'def'])
  expect(r.body).toBe('abcdef')
  expect(r.ended).toBe(false)
})

test('zero-length body ends immediately', () => {
  const r = collect(new ResponseParser(), [
    'HTTP/1.1 204 No Content\r\ncontent-length: 0\r\n\r\n',
  ])
  expect(r.head.status).toBe(204)
  expect(r.ended).toBe(true)
})
