// Preview proxy: https://<port>-<sid>.<previewDomain>/... -> tunnel stream ->
// daemon -> container:port. HTTP/1.1 only, Connection: close per request.
// WebSocket upgrades (HMR) are not supported in v1.
import type { HostHub } from "./tunnel.ts";
import type { Store } from "./store.ts";
import { Tokens } from "./tokens.ts";

export const COOKIE = "devagents_preview";
const COOKIE_TTL_MS = 12 * 60 * 60 * 1000;

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

  async handle(req: Request, { port, sid }: { port: number; sid: string }): Promise<Response> {
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

    if (req.headers.get("upgrade")?.toLowerCase() === "websocket") {
      return new Response("websocket preview is not supported in v1", { status: 501 });
    }

    const s = this.store.session(sid);
    if (!s || s.status !== "running" || !s.host_id) return new Response("session is not running", { status: 502 });
    if (!this.hub.isOnline(s.host_id)) return new Response("host is offline", { status: 502 });

    return proxyOnce(req, port, (sub) => this.hub.dialPort(s.host_id!, sid, port, sub));
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

const HOP = new Set(["connection", "keep-alive", "transfer-encoding", "upgrade", "proxy-connection", "te", "trailer"]);

export async function proxyOnce(
  req: Request, port: number,
  dial: (sub: { onData(d: Uint8Array): void; onClose(): void }) => Promise<{ write(d: Uint8Array): void; close(): void }>,
): Promise<Response> {
  const url = new URL(req.url);
  const body = req.body ? new Uint8Array(await req.arrayBuffer()) : null;

  const lines = [`${req.method} ${url.pathname}${url.search} HTTP/1.1`, `host: localhost:${port}`, "connection: close"];
  for (const [k, v] of req.headers) {
    if (HOP.has(k) || k === "host" || k === "content-length") continue;
    if (k === "cookie") {
      const kept = v.split(";").map((s) => s.trim()).filter((s) => !s.startsWith(`${COOKIE}=`));
      if (kept.length) lines.push(`cookie: ${kept.join("; ")}`);
      continue;
    }
    lines.push(`${k}: ${v}`);
  }
  if (body) lines.push(`content-length: ${body.length}`);
  const head = new TextEncoder().encode(lines.join("\r\n") + "\r\n\r\n");

  const parser = new ResponseParser();
  const headers = Promise.withResolvers<{ status: number; headers: Headers }>();
  // A failed dial rejects `dial()` *and* fires onClose, which rejects this promise
  // after we have already returned 502. Mark it handled or the process dies.
  headers.promise.catch(() => {});
  let controller: ReadableStreamDefaultController<Uint8Array> | null = null;
  let finished = false;
  const finish = () => { if (finished) return; finished = true; try { controller?.close(); } catch {} };

  const stream = new ReadableStream<Uint8Array>({ start(c) { controller = c; }, cancel() { conn?.close(); } });
  let conn: { write(d: Uint8Array): void; close(): void } | null = null;

  try {
    conn = await dial({
      onData: (d) => {
        for (const ev of parser.feed(d)) {
          if (ev.kind === "head") headers.resolve({ status: ev.status, headers: ev.headers });
          else if (ev.kind === "body") { try { controller?.enqueue(ev.data); } catch {} }
          else finish();
        }
      },
      onClose: () => {
        if (!parser.headDone) headers.reject(new Error("upstream closed before response headers"));
        finish();
      },
    });
  } catch (e) {
    return new Response(`could not reach port ${port} in the sandbox: ${String(e)}`, { status: 502 });
  }

  conn.write(head);
  if (body) conn.write(body);

  try {
    const { status, headers: h } = await headers.promise;
    const out = new Headers();
    for (const [k, v] of h) if (!HOP.has(k) && k !== "content-length") out.append(k, v);
    if (status === 204 || status === 304) { finish(); conn.close(); return new Response(null, { status, headers: out }); }
    return new Response(stream, { status, headers: out });
  } catch (e) {
    return new Response(`bad upstream response: ${String(e)}`, { status: 502 });
  }
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
