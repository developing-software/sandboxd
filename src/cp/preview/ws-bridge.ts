// WebSocket bridging for the preview proxy. The browser side is a Bun server
// WebSocket; the sandbox side is a raw HTTP/1.1 upgrade over a tunnel stream.
// Frames are re-encoded in between (no extensions are negotiated upstream).
import type { ServerWebSocket } from "bun";
import { OP, WsFrameParser, decodeClose, encodeClose, encodeWsFrame, wsAccept, wsKey } from "./wsframe.ts";
import { passHeaders, requestHead, sendHead, type Dial, type Upstream, type UpstreamResponse } from "./http.ts";
import { logger } from "../../shared/log.ts";

const log = logger("preview");

export const MAX_WS_MESSAGE = 16 * 1024 * 1024;

export interface PreviewWsData { kind: "preview"; bridge: PreviewBridge }
type WS = ServerWebSocket<PreviewWsData>;
export type PreviewUpgrader = { upgrade(req: Request, opts: { data: PreviewWsData; headers?: Record<string, string> }): boolean };

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

/** Returns undefined when the request was upgraded to a WebSocket. */
export async function proxyWebSocket(req: Request, port: number, dial: Dial, server: PreviewUpgrader): Promise<Response | undefined> {
  const key = wsKey();
  const head = requestHead(req, port, [
    "connection: Upgrade", "upgrade: websocket", `sec-websocket-key: ${key}`, "sec-websocket-version: 13",
  ]);

  let bridge: PreviewBridge | null = null;
  const early: Uint8Array[] = [];
  let closedEarly = false;
  let res: UpstreamResponse;
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
