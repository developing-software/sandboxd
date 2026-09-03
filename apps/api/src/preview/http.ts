// Plain HTTP forwarding for the preview proxy: one upstream connection per
// request (HTTP/1.1, Connection: close), response body streamed back.
import type { PortHandle } from '../hosts/conn'
import { ResponseParser } from './response-parser'
import { concat } from '@sandboxd/core/bytes'

export type Upstream = PortHandle
export type Dial = (sub: { onData(d: Uint8Array): void; onClose(): void }) => Promise<Upstream>

export const COOKIE = 'sandboxd_preview'

const HOP = new Set([
  'connection',
  'keep-alive',
  'transfer-encoding',
  'upgrade',
  'proxy-connection',
  'te',
  'trailer',
  'sec-websocket-key',
  'sec-websocket-version',
  'sec-websocket-extensions',
  'sec-websocket-accept',
])

/** Request head for the upstream: our cookie stripped, the original host forwarded. */
export function requestHead(req: Request, port: number, extra: string[]): Uint8Array {
  const url = new URL(req.url)
  const lines = [
    `${req.method} ${url.pathname}${url.search} HTTP/1.1`,
    `host: localhost:${port}`,
    ...extra,
  ]
  for (const [k, v] of req.headers) {
    if (HOP.has(k) || k === 'host' || k === 'content-length') continue
    if (k === 'cookie') {
      const kept = v
        .split(';')
        .map((s) => s.trim())
        .filter((s) => !s.startsWith(`${COOKIE}=`))
      if (kept.length) lines.push(`cookie: ${kept.join('; ')}`)
      continue
    }
    lines.push(`${k}: ${v}`)
  }
  const host = req.headers.get('host')
  if (host && !req.headers.has('x-forwarded-host')) lines.push(`x-forwarded-host: ${host}`)
  if (!req.headers.has('x-forwarded-proto'))
    lines.push(`x-forwarded-proto: ${url.protocol.replace(':', '')}`)
  return new TextEncoder().encode(lines.join('\r\n') + '\r\n\r\n')
}

export interface UpstreamResponse {
  conn: Upstream
  status: number
  headers: Headers
}

/** Dial, write the head, resolve once the response head is parsed. Everything
 *  after the head is parsed as body and handed to `onBody` / `onEnd`. For a 101
 *  the parser is in until-close mode, so "body" is simply the raw socket bytes. */
export async function sendHead(
  head: Uint8Array,
  dial: Dial,
  onBody: (d: Uint8Array) => void,
  onEnd: () => void,
  onClose: () => void,
): Promise<UpstreamResponse> {
  const parser = new ResponseParser()
  const headP = Promise.withResolvers<{ status: number; headers: Headers }>()
  // A failed dial rejects `dial()` *and* fires onClose, which rejects this promise
  // after we have already returned 502. Mark it handled or the process dies.
  headP.promise.catch(() => {})
  const conn = await dial({
    onData: (d) => {
      for (const ev of parser.feed(d)) {
        if (ev.kind === 'head') headP.resolve({ status: ev.status, headers: ev.headers })
        else if (ev.kind === 'body') onBody(ev.data)
        else onEnd()
      }
    },
    onClose: () => {
      if (!parser.headDone) headP.reject(new Error('upstream closed before response headers'))
      onClose()
    },
  })
  conn.write(head)
  const { status, headers } = await headP.promise
  return { conn, status, headers }
}

export function passHeaders(h: Headers): Headers {
  const out = new Headers()
  for (const [k, v] of h) if (!HOP.has(k) && k !== 'content-length') out.append(k, v)
  return out
}

export async function proxyOnce(req: Request, port: number, dial: Dial): Promise<Response> {
  const body = req.body ? new Uint8Array(await req.arrayBuffer()) : null
  const head = requestHead(req, port, [
    'connection: close',
    ...(body ? [`content-length: ${body.length}`] : []),
  ])
  const out = body ? concat([head, body]) : head

  let controller: ReadableStreamDefaultController<Uint8Array> | null = null
  let finished = false
  const finish = () => {
    if (finished) return
    finished = true
    try {
      controller?.close()
    } catch {}
  }
  let conn: Upstream | null = null
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c
    },
    cancel() {
      conn?.close()
    },
  })

  let res: UpstreamResponse
  try {
    res = await sendHead(
      out,
      dial,
      (d) => {
        try {
          controller?.enqueue(d)
        } catch {}
      },
      finish,
      finish,
    )
  } catch (e) {
    const msg = String(e)
    if (msg.includes('upstream closed before response headers'))
      return new Response(`bad upstream response: ${msg}`, { status: 502 })
    return new Response(`could not reach port ${port} in the sandbox: ${msg}`, { status: 502 })
  }
  conn = res.conn
  const headers = passHeaders(res.headers)
  if (res.status === 204 || res.status === 304) {
    finish()
    conn.close()
    return new Response(null, { status: res.status, headers })
  }
  return new Response(stream, { status: res.status, headers })
}
