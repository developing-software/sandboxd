import { expect, test } from 'bun:test'
import { Id } from '../src/ids'

test('session ids are dns-safe base32', () => {
  for (let i = 0; i < 50; i++) expect(Id.session()).toMatch(/^s_[a-z2-7]{26}$/)
  expect(Id.host()).toMatch(/^h_[a-z2-7]{26}$/)
})

test('approve codes avoid ambiguous glyphs', () => {
  for (let i = 0; i < 100; i++)
    expect(Id.code()).toMatch(
      /^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{2}$/,
    )
})
