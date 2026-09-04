import { expect, test } from 'bun:test'
import { Frame } from '../src/framing'

test('round-trips stream id and payload', () => {
  const payload = new TextEncoder().encode('hello\x1b[31m')
  const frame = Frame.encode(0xdeadbeef, payload)
  const out = Frame.decode(frame)
  expect(out.stream).toBe(0xdeadbeef)
  expect(new TextDecoder().decode(out.payload)).toBe('hello\x1b[31m')
})

test('decodes from an offset view', () => {
  const frame = Frame.encode(7, new Uint8Array([1, 2, 3]))
  const padded = new Uint8Array(3 + frame.length)
  padded.set(frame, 3)
  const out = Frame.decode(padded.subarray(3))
  expect(out.stream).toBe(7)
  expect([...out.payload]).toEqual([1, 2, 3])
})

test('rejects short frames', () => {
  expect(() => Frame.decode(new Uint8Array([0, 0]))).toThrow()
})
