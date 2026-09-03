// Preview proxy: https://<port>-<sid>.<previewDomain>/... -> tunnel stream ->
// daemon -> container:port. HTTP/1.1 only, Connection: close per request.
// WebSocket upgrades are bridged: the browser side is a Bun server WebSocket,
// the sandbox side is a raw HTTP/1.1 upgrade over a tunnel stream; frames are
// re-encoded in between (no extensions are negotiated upstream).
import type { ServerWebSocket } from "bun";
import type { HostHub, PortHandle } from "./tunnel.ts";
import type { Store } from "./store.ts";
import { Tokens } from "./tokens.ts";
import { OP, WsFrameParser, decodeClose, encodeClose, encodeWsFrame, wsAccept, wsKey } from "./wsframe.ts";
import { logger } from "../shared/log.ts";

const log = logger("preview");

export const COOKIE = "devagents_preview";
const COOKIE_TTL_MS = 12 * 60 * 60 * 1000;
export const MAX_WS_MESSAGE = 16 * 1024 * 1024;

type Upstream = PortHandle;
export type Dial = (sub: { onData(d: Uint8Array): void; onClose(): void }) => Promise<Upstream>;
export type PreviewUpgrader = { upgrade(req: Request, opts: { data: PreviewWsData; headers?: Record<string, string> }): boolean };

export class PreviewProxy {
  private hostRe: RegExp;

  constructor(private store: Store, private hub: HostHub, private tokens: Tokens, private previewDomain: string) {
    this.hostRe = new RegExp(`^(\\d{1,5})-(s_[a-z2-7]+)\\.${previewDomain.replace(/\./g, "\\.")}(?::\\d+)?$`, "i");
  }

  /** Returns null when the Host header is not a preview subdomain. */
  match(req: Request): { port: number; sid: string } | null {
    const m = this.hostRe.exec(req.headers.get("host") ?? "");
    return m ? { port: Number(m[1]), sid: m[2]!.toLowerCase() } : null;
  }

  /** Returns undefined when the request was upgraded to a WebSocket. */
  async handle(req: Request, { port, sid }: { port: number; sid: string }, server: PreviewUpgrader): Promise<Response | undefined> {
    const url = new URL(req.url);
    const secure = url.protocol === "https:" || req.headers.get("x-forwarded-proto") === "https";

    // First visit: ?t=<preview token> -> set cookie -> redirect without it.
    const t = url.searchParams.get("t");
    if (t) {
      const p = this.tokens.verify(t, "preview");
      if (!p || p.sid !== sid || p.port !== port) return new Response("invalid or expired preview token", { status: 401 });
      url.searchParams.delete("t");
      const cookie = this.tokens.sign({ k: "preview-cookie", sid, port }, COOKIE_TTL_MS);
      return new Response(null, {
        status: 302,
        headers: {
          location: url.pathname + url.search,
          "set-cookie": `${COOKIE}=${cookie}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${COOKIE_TTL_MS / 1000}${secure ? "; Secure" : ""}`,
        },
      });
    }

    const cookies = parseCookies(req.headers.get("cookie"));
    const c = this.tokens.verify(cookies[COOKIE], "preview-cookie");
    if (!c || c.sid !== sid || c.port !== port) return new Response("preview requires a token: open the link from the app", { status: 401 });

    const s = this.store.session(sid);
    if (!s || s.status !== "running" || !s.host_id) return new Response("session is not running", { status: 502 });
    if (!this.hub.isOnline(s.host_id)) return new Response("host is offline", { status: 502 });
    const dial: Dial = (sub) => this.hub.dialPort(s.host_id!, sid, port, sub);

    if (req.headers.get("upgrade")?.toLowerCase() === "websocket") return proxyWebSocket(req, port, dial, server);
    return proxyOnce(req, port, dial);
  }
}

function parseCookies(h: string | null): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of (h ?? "").split(";")) {
    const i = part.indexOf("=");
    if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim();
  }
  return out;
}

const HOP = new Set(["connection", "keep-alive", "transfer-encoding", "upgrade", "proxy-connection", "te", "trailer",
  "sec-websocket-key", "sec-websocket-version", "sec-websocket-extensions", "sec-websocket-accept"]);

/** Request head for the upstream: our cookie stripped, the original host forwarded. */
function requestHead(req: Request, port: number, extra: string[]): Uint8Array {
  const url = new URL(req.url);
  const lines = [`${req.method} ${url.pathname}${url.search} HTTP/1.1`, `host: localhost:${port}`, ...extra];
  for (const [k, v] of req.headers) {
    if (HOP.has(k) || k === "host" || k === "content-length") continue;
    if (k === "cookie") {
      const kept = v.split(";").map((s) => s.trim()).filter((s) => !s.startsWith(`${COOKIE}=`));
      if (kept.length) lines.push(`cookie: ${kept.join("; ")}`);
      continue;
    }
    lines.push(`${k}: ${v}`);
  }
  const host = req.headers.get("host");
  if (host && !req.headers.has("x-forwarded-host")) lines.push(`x-forwarded-host: ${host}`);
  if (!req.headers.has("x-forwarded-proto")) lines.push(`x-forwarded-proto: ${url.protocol.replace(":", "")}`);
  return new TextEncoder().encode(lines.join("\r\n") + "\r\n\r\n");
}

/** Dial, write the head, resolve once the response head is parsed. Everything
 *  after the head is parsed as body and handed to `onBody` / `onEnd`. For a 101
 *  the parser is in until-close mode, so "body" is simply the raw socket bytes. */
async function sendHead(
  head: Uint8Array, dial: Dial,
  onBody: (d: Uint8Array) => void, onEnd: () => void, onClose: () => void,
): Promise<{ conn: Upstream; status: number; headers: Headers }> {
  const parser = new ResponseParser();
  const headP = Promise.withResolvers<{ status: number; headers: Headers }>();
  // A failed dial rejects `dial()` *and* fires onClose, which rejects this promise
  // after we have already returned 502. Mark it handled or the process dies.
  headP.promise.catch(() => {});
  const conn = await dial({
    onData: (d) => {
      for (const ev of parser.feed(d)) {
        if (ev.kind === "head") headP.resolve({ status: ev.status, headers: ev.headers });
        else if (ev.kind === "body") onBody(ev.data);
        else onEnd();
      }
    },
    onClose: () => {
      if (!parser.headDone) headP.reject(new Error("upstream closed before response headers"));
      onClose();
    },
  });
  conn.write(head);
  const { status, headers } = await headP.promise;
  return { conn, status, headers };
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length);
  out.set(a); out.set(b, a.length);
  return out;
}

function passHeaders(h: Headers): Headers {
  const out = new Headers();
  for (const [k, v] of h) if (!HOP.has(k) && k !== "content-length") out.append(k, v);
  return out;
}

export async function proxyOnce(req: Request, port: number, dial: Dial): Promise<Response> {
  const body = req.body ? new Uint8Array(await req.arrayBuffer()) : null;
  const head = requestHead(req, port, ["connection: close", ...(body ? [`content-length: ${body.length}`] : [])]);
  const out = body ? concat(head, body) : head;

  let controller: ReadableStreamDefaultController<Uint8Array> | null = null;
  let finished = false;
  const finish = () => { if (finished) return; finished = true; try { controller?.close(); } catch {} };
  let conn: Upstream | null = null;
  const stream = new ReadableStream<Uint8Array>({ start(c) { controller = c; }, cancel() { conn?.close(); } });

  let res: Awaited<ReturnType<typeof sendHead>>;
  try {
    res = await sendHead(out, dial, (d) => { try { controller?.enqueue(d); } catch {} }, finish, finish);
  } catch (e) {
    const msg = String(e);
    if (msg.includes("upstream closed before response headers")) return new Response(`bad upstream response: ${msg}`, { status: 502 });
    return new Response(`could not reach port ${port} in the sandbox: ${msg}`, { status: 502 });
  }
  conn = res.conn;
  const headers = passHeaders(res.headers);
  if (res.status === 204 || res.status === 304) { finish(); conn.close(); return new Response(null, { status: res.status, headers }); }
  return new Response(stream, { status: res.status, headers });
}

// ---- WebSocket bridge ------------------------------------------------------

export interface PreviewWsData { kind: "preview"; bridge: PreviewBridge }
type WS = ServerWebSocket<PreviewWsData>;

/** One bridged connection. Upstream bytes may arrive before Bun fires `open`,
 *  so they are queued until the browser socket is attached. */
export class PreviewBridge {
  private ws: WS | null = null;
  private pending: (() => void)[] = [];
  private frames = new WsFrameParser(MAX_WS_MESSAGE);
  private upstreamClosed = false;

  constructor(private conn: Upstream) {}

  /** Bytes from the sandbox after the 101. */
  onUpstreamData(d: Uint8Array) {
    let msgs;
    try { msgs = this.frames.feed(d); } catch (e) { this.fail(1002, String(e)); return; }
    for (const m of msgs) this.run(() => this.deliver(m));
  }

  onUpstreamClose() {
    if (this.upstreamClosed) return;
    this.upstreamClosed = true;
    this.run(() => { try { this.ws?.close(1001, "upstream closed"); } catch {} });
  }

  attach(ws: WS) {
    this.ws = ws;
    for (const f of this.pending) f();
    this.pending = [];
  }

  onBrowserMessage(msg: string | Buffer) {
    if (this.upstreamClosed) return;
    const payload = typeof msg === "string" ? new TextEncoder().encode(msg) : new Uint8Array(msg.buffer, msg.byteOffset, msg.byteLength);
    this.conn.write(encodeWsFrame(typeof msg === "string" ? OP.TEXT : OP.BINARY, payload, true));
  }

  onBrowserClose(code: number, reason: string) {
    if (this.upstreamClosed) return;
    this.upstreamClosed = true;
    try { this.conn.write(encodeWsFrame(OP.CLOSE, encodeClose(validCloseCode(code) ? code : 1000, reason), true)); } catch {}
    this.conn.close();
  }

  private run(f: () => void) { this.ws ? f() : this.pending.push(f); }

  private deliver(m: { opcode: number; data: Uint8Array }) {
    const ws = this.ws!;
    switch (m.opcode) {
      case OP.TEXT: ws.send(new TextDecoder().decode(m.data)); break;
      case OP.BINARY: ws.send(m.data); break;
      case OP.PING: if (!this.upstreamClosed) this.conn.write(encodeWsFrame(OP.PONG, m.data, true)); break;
      case OP.PONG: break;
      case OP.CLOSE: {
        const { code, reason } = decodeClose(m.data);
        if (!this.upstreamClosed) {
          this.upstreamClosed = true;
          try { this.conn.write(encodeWsFrame(OP.CLOSE, m.data, true)); } catch {}
          this.conn.close();
        }
        try { ws.close(validCloseCode(code) ? code : 1000, reason); } catch {}
        break;
      }
    }
  }

  private fail(code: number, reason: string) {
    log.warn("websocket bridge error", { reason });
    if (!this.upstreamClosed) { this.upstreamClosed = true; this.conn.close(); }
    this.run(() => { try { this.ws?.close(code, reason.slice(0, 120)); } catch {} });
  }
}

function validCloseCode(c: number) { return (c >= 1000 && c <= 1003) || (c >= 1007 && c <= 1014) || (c >= 3000 && c <= 4999); }

async function proxyWebSocket(req: Request, port: number, dial: Dial, server: PreviewUpgrader): Promise<Response | undefined> {
  const key = wsKey();
  const head = requestHead(req, port, [
    "connection: Upgrade", "upgrade: websocket", `sec-websocket-key: ${key}`, "sec-websocket-version: 13",
  ]);

  let bridge: PreviewBridge | null = null;
  const early: Uint8Array[] = [];
  let closedEarly = false;
  let res: Awaited<ReturnType<typeof sendHead>>;
  try {
    const onClose = () => { bridge ? bridge.onUpstreamClose() : (closedEarly = true); };
    res = await sendHead(head, dial, (d) => { bridge ? bridge.onUpstreamData(d) : early.push(d); }, onClose, onClose);
  } catch (e) {
    return new Response(`could not reach port ${port} in the sandbox: ${String(e)}`, { status: 502 });
  }

  if (res.status !== 101) {
    // Upstream refused the upgrade: pass its answer through as a plain response.
    res.conn.close();
    return new Response(`upstream refused websocket upgrade (${res.status})`, { status: res.status, headers: passHeaders(res.headers) });
  }
  if (res.headers.get("sec-websocket-accept") !== wsAccept(key)) {
    res.conn.close();
    return new Response("bad upstream websocket handshake", { status: 502 });
  }

  bridge = new PreviewBridge(res.conn);
  const acceptedProto = res.headers.get("sec-websocket-protocol");
  // Bun rejects an empty headers object, so only pass one when there is something to say.
  const headers = acceptedProto && req.headers.has("sec-websocket-protocol") ? { "sec-websocket-protocol": acceptedProto } : undefined;
  if (!server.upgrade(req, { data: { kind: "preview", bridge }, ...(headers ? { headers } : {}) })) {
    res.conn.close();
    return new Response("websocket upgrade failed", { status: 500 });
  }
  for (const d of early) bridge.onUpstreamData(d);
  if (closedEarly) bridge.onUpstreamClose();
  return undefined;
}

/** Incremental HTTP/1.1 response parser: head, then body (chunked, length-delimited, or until close). */
type ParseEvent = { kind: "head"; status: number; headers: Headers } | { kind: "body"; data: Uint8Array } | { kind: "end" };

export class ResponseParser {
  headDone = false;
  private buf = new Uint8Array(0);
  private mode: "chunked" | "length" | "close" = "close";
  private remaining = 0; // for length mode, or current chunk in chunked mode
  private chunkState: "size" | "data" | "crlf" | "done" = "size";

  feed(data: Uint8Array): ParseEvent[] {
    const merged = new Uint8Array(this.buf.length + data.length);
    merged.set(this.buf); merged.set(data, this.buf.length);
    this.buf = merged;
    const events: ParseEvent[] = [];

    if (!this.headDone) {
      const idx = findCRLF2(this.buf);
      if (idx < 0) return events;
      const head = new TextDecoder().decode(this.buf.subarray(0, idx));
      this.buf = this.buf.subarray(idx + 4);
      const [statusLine, ...rest] = head.split("\r\n");
      const status = Number(statusLine?.split(" ")[1] ?? 502);
      const headers = new Headers();
      for (const l of rest) { const i = l.indexOf(":"); if (i > 0) headers.append(l.slice(0, i).trim(), l.slice(i + 1).trim()); }
      this.headDone = true;
      const te = headers.get("transfer-encoding")?.toLowerCase();
      const cl = headers.get("content-length");
      if (te?.includes("chunked")) this.mode = "chunked";
      else if (cl !== null) { this.mode = "length"; this.remaining = Number(cl); }
      events.push({ kind: "head", status, headers });
      if (this.mode === "length" && this.remaining === 0) { events.push({ kind: "end" }); return events; }
    }

    if (this.mode === "close") {
      if (this.buf.length) { events.push({ kind: "body", data: this.buf }); this.buf = new Uint8Array(0); }
      return events;
    }
    if (this.mode === "length") {
      const take = Math.min(this.remaining, this.buf.length);
      if (take) { events.push({ kind: "body", data: this.buf.subarray(0, take) }); this.buf = this.buf.subarray(take); this.remaining -= take; }
      if (this.remaining === 0) events.push({ kind: "end" });
      return events;
    }
    // chunked
    while (true) {
      if (this.chunkState === "size") {
        const i = findCRLF(this.buf);
        if (i < 0) break;
        const size = parseInt(new TextDecoder().decode(this.buf.subarray(0, i)).split(";")[0]!.trim(), 16);
        this.buf = this.buf.subarray(i + 2);
        if (size === 0) { this.chunkState = "done"; events.push({ kind: "end" }); break; }
        this.remaining = size; this.chunkState = "data";
      } else if (this.chunkState === "data") {
        const take = Math.min(this.remaining, this.buf.length);
        if (take) { events.push({ kind: "body", data: this.buf.subarray(0, take) }); this.buf = this.buf.subarray(take); this.remaining -= take; }
        if (this.remaining > 0) break;
        this.chunkState = "crlf";
      } else if (this.chunkState === "crlf") {
        if (this.buf.length < 2) break;
        this.buf = this.buf.subarray(2); this.chunkState = "size";
      } else break;
    }
    return events;
  }
}

function findCRLF(b: Uint8Array) { for (let i = 0; i + 1 < b.length; i++) if (b[i] === 13 && b[i + 1] === 10) return i; return -1; }
function findCRLF2(b: Uint8Array) { for (let i = 0; i + 3 < b.length; i++) if (b[i] === 13 && b[i + 1] === 10 && b[i + 2] === 13 && b[i + 3] === 10) return i; return -1; }
