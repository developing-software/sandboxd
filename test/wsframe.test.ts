import { expect, test } from "bun:test";
import { OP, WsFrameParser, decodeClose, encodeClose, encodeWsFrame, wsAccept } from "../src/cp/wsframe.ts";

const enc = (s: string) => new TextEncoder().encode(s);
const dec = (b: Uint8Array) => new TextDecoder().decode(b);

test("round trip: unmasked and masked, all three length encodings", () => {
  for (const n of [0, 5, 125, 126, 300, 65535, 65536, 100_000]) {
    const payload = new Uint8Array(n).map((_, i) => i & 0xff);
    for (const mask of [false, true]) {
      const frame = encodeWsFrame(OP.BINARY, payload, mask);
      expect((frame[1]! & 0x80) !== 0).toBe(mask);
      const [m] = new WsFrameParser().feed(frame);
      expect(m!.opcode).toBe(OP.BINARY);
      expect(m!.data).toEqual(payload);
    }
  }
});

test("byte-at-a-time feeding and several frames per read", () => {
  const a = encodeWsFrame(OP.TEXT, enc("hello"), true), b = encodeWsFrame(OP.PING, enc("p"), false), c = encodeWsFrame(OP.TEXT, enc("world"), true);
  const all = new Uint8Array([...a, ...b, ...c]);
  const p = new WsFrameParser();
  const got: { opcode: number; s: string }[] = [];
  for (const byte of all) for (const m of p.feed(new Uint8Array([byte]))) got.push({ opcode: m.opcode, s: dec(m.data) });
  expect(got).toEqual([{ opcode: OP.TEXT, s: "hello" }, { opcode: OP.PING, s: "p" }, { opcode: OP.TEXT, s: "world" }]);
  expect(new WsFrameParser().feed(all).map((m) => dec(m.data))).toEqual(["hello", "p", "world"]);
});

test("fragmented message is reassembled; control frames may interleave", () => {
  const first = encodeWsFrame(OP.TEXT, enc("ab"), false); first[0] = OP.TEXT;          // FIN=0
  const ping = encodeWsFrame(OP.PING, new Uint8Array(0), false);
  const mid = encodeWsFrame(OP.CONT, enc("cd"), false); mid[0] = OP.CONT;               // FIN=0
  const last = encodeWsFrame(OP.CONT, enc("ef"), false);                                 // FIN=1
  const got = new WsFrameParser().feed(new Uint8Array([...first, ...ping, ...mid, ...last]));
  expect(got.map((m) => [m.opcode, dec(m.data)])).toEqual([[OP.PING, ""], [OP.TEXT, "abcdef"]]);
});

test("protocol violations and oversize frames throw", () => {
  expect(() => new WsFrameParser().feed(encodeWsFrame(OP.CONT, enc("x"), false))).toThrow(/continuation/);
  const big = encodeWsFrame(OP.BINARY, new Uint8Array(200), false);
  expect(() => new WsFrameParser(100).feed(big)).toThrow(/too large/);
  const frag = encodeWsFrame(OP.BINARY, new Uint8Array(60), false); frag[0] = OP.BINARY;
  const p = new WsFrameParser(100);
  p.feed(frag);
  expect(() => p.feed(encodeWsFrame(OP.CONT, new Uint8Array(60), false))).toThrow(/message too large/);
});

test("close payload and accept key", () => {
  expect(decodeClose(encodeClose(1001, "bye"))).toEqual({ code: 1001, reason: "bye" });
  expect(decodeClose(new Uint8Array(0))).toEqual({ code: 1005, reason: "" });
  // RFC 6455 section 1.3 example
  expect(wsAccept("dGhlIHNhbXBsZSBub25jZQ==")).toBe("s3pPLMBiTxaQ9kYGzzhZRbK+xOo=");
});
