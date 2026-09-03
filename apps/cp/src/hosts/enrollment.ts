// Enrollment: map a `hello` onto a host row and decide what the daemon hears back.
// Unknown fingerprint -> pending row with a human code; approved -> accepted; revoked -> rejected.
import type { HostMsg } from '@sandboxd/core/messages'
import type { HostRow, Store } from '../store'
import { newApproveCode, newHostId } from '@sandboxd/core/ids'

export type Hello = Extract<HostMsg, { type: 'hello' }>

export type Enrollment =
  | { kind: 'accepted'; host: HostRow }
  | { kind: 'pending'; host: HostRow; code: string; isNew: boolean }
  | { kind: 'rejected'; host: HostRow; reason: string }

export function enroll(store: Store, hello: Hello): Enrollment {
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
  switch (host.status) {
    case 'revoked':
      return { kind: 'rejected', host, reason: 'revoked' }
    case 'pending':
      return { kind: 'pending', host, code: host.approve_code!, isNew }
    case 'approved':
      return { kind: 'accepted', host }
  }
}
