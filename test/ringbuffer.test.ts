import { expect, test } from "bun:test";
import { RingBuffer, PtyFanout } from "../src/agent/pty.ts";

const bytes = (s: string) => new TextEncoder().encode(s);
const str = (b: Uint8Array) => new TextDecoder().decode(b);

test("keeps only the most recent bytes", () => {
  const rb = new RingBuffer(10);
  rb.push(bytes("aaaa")); rb.push(bytes("bbbb")); rb.push(bytes("cccc"));
  expect(str(rb.snapshot())).toBe("bbbbcccc");
});

test("a single oversized chunk keeps its tail", () => {
  const rb = new RingBuffer(4);
  rb.push(bytes("0123456789"));
  expect(str(rb.snapshot())).toBe("6789");
});

test("fanout replays to new subscribers and tracks activity", async () => {
  const f = new PtyFanout(100);
  const before = f.lastActivity;
  await Bun.sleep(2);
  f.emit(bytes("hi"));
  expect(f.lastActivity).toBeGreaterThan(before);
  const got: string[] = [];
  const unsub = f.subscribe((d) => got.push(str(d)));
  f.emit(bytes("!"));
  expect(got).toEqual(["!"]);
  expect(str(f.buffer.snapshot())).toBe("hi!");
  unsub();
  f.emit(bytes("x"));
  expect(got).toEqual(["!"]);
  expect(f.viewers).toBe(0);
});
