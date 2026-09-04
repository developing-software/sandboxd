import { expect, test } from 'bun:test'
import { Store } from '../src/store'
import { enroll } from '../src/hosts/enrollment'

const hello = (fp: string, name = 'box', join_token?: string) => ({
  type: 'hello' as const,
  name,
  fingerprint: fp,
  running: [],
  max_sessions: 3,
  ...(join_token === undefined ? {} : { join_token }),
})

test('unknown fingerprint -> pending row with a code; same fingerprint again is still pending, not new', () => {
  const store = new Store(':memory:')
  const e1 = enroll(store, hello('fp1'))
  expect(e1.kind).toBe('pending')
  if (e1.kind !== 'pending') throw new Error()
  expect(e1.isNew).toBe(true)
  expect(e1.badToken).toBe(false)
  expect(e1.code).toMatch(/^[A-Z2-9]{4}-[A-Z2-9]{2}$/)
  expect(store.hostById(e1.host.id)?.status).toBe('pending')
  const e2 = enroll(store, hello('fp1', 'renamed'))
  expect(e2.kind).toBe('pending')
  if (e2.kind !== 'pending') throw new Error()
  expect(e2.isNew).toBe(false)
  expect(e2.host.id).toBe(e1.host.id)
  expect(store.hostById(e1.host.id)?.name).toBe('renamed') // hello refreshes name and capacity
})

test('approved -> accepted; revoked -> rejected', () => {
  const store = new Store(':memory:')
  const e = enroll(store, hello('fp'))
  store.approveHost(e.host.id)
  expect(enroll(store, hello('fp')).kind).toBe('accepted')
  store.revokeHost(e.host.id)
  const r = enroll(store, hello('fp'))
  expect(r.kind).toBe('rejected')
  if (r.kind === 'rejected') expect(r.reason).toBe('revoked')
})

test('join token: a matching token approves an unknown host on the first hello', () => {
  const store = new Store(':memory:')
  const e = enroll(store, hello('fp', 'box', 'jt'), 'jt')
  expect(e.kind).toBe('accepted')
  if (e.kind !== 'accepted') throw new Error()
  expect(e.joined).toBe(true)
  expect(store.hostById(e.host.id)?.status).toBe('approved')
  expect(store.hostById(e.host.id)?.approve_code).toBeNull()
  // The next hello is an ordinary accepted one, token or not.
  const again = enroll(store, hello('fp'))
  expect(again.kind).toBe('accepted')
  if (again.kind === 'accepted') expect(again.joined).toBe(false)
})

test('join token: approves a host that was already pending by code', () => {
  const store = new Store(':memory:')
  const p = enroll(store, hello('fp'))
  expect(p.kind).toBe('pending')
  const e = enroll(store, hello('fp', 'box', 'jt'), 'jt')
  expect(e.kind).toBe('accepted')
  expect(e.host.id).toBe(p.host.id)
  expect(store.hostById(p.host.id)?.status).toBe('approved')
})

test('join token: a wrong or unexpected token falls back to the code, flagged; a revoked host stays revoked', () => {
  const store = new Store(':memory:')
  const wrong = enroll(store, hello('fp', 'box', 'nope'), 'jt')
  expect(wrong.kind).toBe('pending')
  if (wrong.kind === 'pending') expect(wrong.badToken).toBe(true)
  // The CP has no token configured: the field is inert, not an error.
  const inert = enroll(store, hello('fp2', 'box', 'jt'))
  expect(inert.kind).toBe('pending')
  if (inert.kind === 'pending') expect(inert.badToken).toBe(true)
  store.revokeHost(wrong.host.id)
  expect(enroll(store, hello('fp', 'box', 'jt'), 'jt').kind).toBe('rejected')
})
