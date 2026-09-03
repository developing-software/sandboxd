import { expect, test } from 'bun:test'
import { Store } from '../src/store'
import { enroll } from '../src/hosts/enrollment'

const hello = (fp: string, name = 'box') => ({
  type: 'hello' as const,
  name,
  fingerprint: fp,
  running: [],
  max_sessions: 3,
})

test('unknown fingerprint -> pending row with a code; same fingerprint again is still pending, not new', () => {
  const store = new Store(':memory:')
  const e1 = enroll(store, hello('fp1'))
  expect(e1.kind).toBe('pending')
  if (e1.kind !== 'pending') throw new Error()
  expect(e1.isNew).toBe(true)
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
