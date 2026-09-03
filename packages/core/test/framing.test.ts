import { expect, test } from 'bun:test'
import { encodeFrame, decodeFrame } from '../src/framing'

test('round-trips stream id and payload', () => {
  const payload = new TextEncoder().encode('hello\x1b[31m')
  const frame = encodeFrame(0xdeadbeef, payload)
  const out = decodeFrame(frame)
  expect(out.stream).toBe(0xdeadbeef)
  expect(new TextDecoder().decode(out.payload)).toBe('hello\x1b[31m')
})

test('decodes from an offset view', () => {
  const frame = encodeFrame(7, new Uint8Array([1, 2, 3]))
  const padded = new Uint8Array(3 + frame.length)
  padded.set(frame, 3)
  const out = decodeFrame(padded.subarray(3))
  expect(out.stream).toBe(7)
  expect([...out.payload]).toEqual([1, 2, 3])
})

test('rejects short frames', () => {
  expect(() => decodeFrame(new Uint8Array([0, 0]))).toThrow()
})
