// Minimal RFC 6455 frame codec. The preview proxy re-frames between the browser
// (Bun's server-side WebSocket, which hands us whole messages) and the upstream
// socket inside the sandbox (raw bytes over a tunnel stream). No extensions.

export const OP = { CONT: 0, TEXT: 1, BINARY: 2, CLOSE: 8, PING: 9, PONG: 10 } as const;

export interface WsMessage { opcode: number; data: Uint8Array }

/** Encode one unfragmented frame. Client->server frames must be masked. */
export function encodeWsFrame(opcode: number, payload: Uint8Array, mask: boolean): Uint8Array {
  const n = payload.length;
  const extLen = n < 126 ? 0 : n < 65536 ? 2 : 8;
  const out = new Uint8Array(2 + extLen + (mask ? 4 : 0) + n);
  out[0] = 0x80 | (opcode & 0x0f);
  out[1] = (mask ? 0x80 : 0) | (extLen === 0 ? n : extLen === 2 ? 126 : 127);
  let i = 2;
  if (extLen === 2) { out[2] = n >> 8; out[3] = n & 0xff; i = 4; }
  else if (extLen === 8) { new DataView(out.buffer).setBigUint64(2, BigInt(n)); i = 10; }
  if (!mask) { out.set(payload, i); return out; }
  const key = crypto.getRandomValues(new Uint8Array(4));
  out.set(key, i); i += 4;
  for (let j = 0; j < n; j++) out[i + j] = payload[j]! ^ key[j & 3]!;
  return out;
}

export function encodeClose(code: number, reason = ""): Uint8Array {
  const r = new TextEncoder().encode(reason).subarray(0, 123);
  const p = new Uint8Array(2 + r.length);
  p[0] = code >> 8; p[1] = code & 0xff; p.set(r, 2);
  return p;
}

export function decodeClose(p: Uint8Array): { code: number; reason: string } {
  if (p.length < 2) return { code: 1005, reason: "" };
  return { code: (p[0]! << 8) | p[1]!, reason: new TextDecoder().decode(p.subarray(2)) };
}

/** Incremental frame parser. Reassembles fragmented data messages; control
 *  frames are emitted as they arrive. Throws on protocol violations or oversize. */
export class WsFrameParser {
  private buf: Uint8Array = new Uint8Array(0);
  private fragOp = -1;
  private frag: Uint8Array[] = [];
  private fragBytes = 0;

  constructor(private maxMessage = 16 * 1024 * 1024) {}

  feed(data: Uint8Array): WsMessage[] {
    if (this.buf.length) {
      const m = new Uint8Array(this.buf.length + data.length);
      m.set(this.buf); m.set(data, this.buf.length); this.buf = m;
    } else this.buf = data;
    const out: WsMessage[] = [];
    while (true) {
      const b = this.buf;
      if (b.length < 2) break;
      const fin = (b[0]! & 0x80) !== 0, opcode = b[0]! & 0x0f, masked = (b[1]! & 0x80) !== 0;
      let len = b[1]! & 0x7f, i = 2;
      if (len === 126) { if (b.length < 4) break; len = (b[2]! << 8) | b[3]!; i = 4; }
      else if (len === 127) {
        if (b.length < 10) break;
        const big = new DataView(b.buffer, b.byteOffset).getBigUint64(2);
        if (big > BigInt(this.maxMessage)) throw new Error("websocket frame too large");
        len = Number(big); i = 10;
      }
      if (len > this.maxMessage) throw new Error("websocket frame too large");
      const total = i + (masked ? 4 : 0) + len;
      if (b.length < total) break;
      let payload = b.subarray(i + (masked ? 4 : 0), total);
      if (masked) {
        const key = b.subarray(i, i + 4), p = new Uint8Array(len);
        for (let j = 0; j < len; j++) p[j] = payload[j]! ^ key[j & 3]!;
        payload = p;
      } else payload = payload.slice();
      this.buf = b.subarray(total);

      if (opcode >= 8) { // control: never fragmented
        if (!fin || len > 125) throw new Error("bad control frame");
        out.push({ opcode, data: payload });
        continue;
      }
      if (opcode === OP.CONT) {
        if (this.fragOp < 0) throw new Error("continuation without start");
      } else {
        if (this.fragOp >= 0) throw new Error("new data frame during fragmented message");
        this.fragOp = opcode;
      }
      this.frag.push(payload); this.fragBytes += len;
      if (this.fragBytes > this.maxMessage) throw new Error("websocket message too large");
      if (!fin) continue;
      const data = this.frag.length === 1 ? this.frag[0]! : concat(this.frag, this.fragBytes);
      out.push({ opcode: this.fragOp, data });
      this.fragOp = -1; this.frag = []; this.fragBytes = 0;
    }
    return out;
  }
}

function concat(parts: Uint8Array[], size: number): Uint8Array {
  const out = new Uint8Array(size);
  let o = 0;
  for (const p of parts) { out.set(p, o); o += p.length; }
  return out;
}

const GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
export function wsAccept(key: string): string {
  return new Bun.CryptoHasher("sha1").update(key + GUID).digest("base64");
}
export function wsKey(): string {
  return Buffer.from(crypto.getRandomValues(new Uint8Array(16))).toString("base64");
}
