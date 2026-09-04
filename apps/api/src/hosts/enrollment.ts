// Enrollment: map a `hello` onto a host row and decide what the daemon hears back.
// Unknown fingerprint -> pending row with a human code; approved -> accepted; revoked -> rejected.
// A matching join token approves on the spot; a wrong one is not a rejection, the daemon
// simply falls back to the printed code (and the hub logs it).
import { timingSafeEqual } from 'node:crypto'
import type { HostMsg } from '@sandboxd/core/messages'
import type { HostRow, Store } from '../store'
import { newApproveCode, newHostId } from '@sandboxd/core/ids'

export type Hello = Extract<HostMsg, { type: 'hello' }>

export type Enrollment =
  | { kind: 'accepted'; host: HostRow; joined: boolean }
  | { kind: 'pending'; host: HostRow; code: string; isNew: boolean; badToken: boolean }
  | { kind: 'rejected'; host: HostRow; reason: string }

export function enroll(
  store: Store,
  hello: Hello,
  joinToken: string | null = null,
): Enrollment {
  const joined = tokenMatches(joinToken, hello.join_token)
  let host = store.hostByFingerprint(hello.fingerprint)
  const isNew = !host
  if (!host) {
    host = store.insertPendingHost({
      id: newHostId(),
      name: hello.name,
      fingerprint: hello.fingerprint,
      approve_code: newApproveCode(),
      max_sessions: hello.max_sessions,
    })
  }
  store.touchHost(host.id, { name: hello.name, max_sessions: hello.max_sessions })
  if (host.status === 'revoked') return { kind: 'rejected', host, reason: 'revoked' }
  if (host.status === 'approved') return { kind: 'accepted', host, joined: false }
  if (!joined) {
    const badToken = hello.join_token !== undefined
    return { kind: 'pending', host, code: host.approve_code!, isNew, badToken }
  }
  store.approveHost(host.id)
  return { kind: 'accepted', host: store.hostById(host.id)!, joined: true }
}

function tokenMatches(expected: string | null, given: string | undefined): boolean {
  if (!expected || !given) return false
  const a = Buffer.from(expected)
  const b = Buffer.from(given)
  return a.length === b.length && timingSafeEqual(a, b)
}
