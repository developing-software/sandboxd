import { expect, test } from 'bun:test'
import { newSessionId, newHostId, newApproveCode } from '../src/ids'

test('session ids are dns-safe base32', () => {
  for (let i = 0; i < 50; i++) expect(newSessionId()).toMatch(/^s_[a-z2-7]{26}$/)
  expect(newHostId()).toMatch(/^h_[a-z2-7]{26}$/)
})

test('approve codes avoid ambiguous glyphs', () => {
  for (let i = 0; i < 100; i++)
    expect(newApproveCode()).toMatch(
      /^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{2}$/,
    )
})
