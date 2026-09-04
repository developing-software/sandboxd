import { expect, test } from 'bun:test'
import { Store } from '../src/store'
import { Scheduler } from '../src/scheduler'
import { Tokens } from '../src/tokens'
import { SessionService } from '../src/sessions'
import { CreateSession } from '../src/schema'
import { Env } from '../src/env'
import type { HostPlacement } from '../src/hosts/hub'
import type { Msg } from '@sandboxd/core/messages'

class NoHosts implements HostPlacement {
  isOnline() {
    return false
  }
  capacity() {
    return null
  }
  createSession(_h: string, _s: Msg.Spec) {
    return false
  }
  destroySession() {}
}

function setup() {
  const store = new Store(':memory:')
  const sched = new Scheduler(store, new NoHosts())
  const cfg = {
    publicUrl: 'https://cp.example.com',
    previewDomain: 'preview.example.com',
  }
  return { store, svc: new SessionService(cfg, store, new NoHosts(), sched, new Tokens('k')) }
}

/** Bodies here go through the same schema the route uses, so defaults are filled in. */
const body = (b: unknown) => CreateSession.parse(b)

test('create: generic body, caller env kept, secrets never stored', () => {
  const { store, svc } = setup()
  const v = svc.create(
    body({
      owner_id: 'me',
      image: 'img:1',
      env: { REPO: 'https://x/r.git', PROMPT: 'p' },
      secret_env: { GIT_TOKEN: 'gt' },
    }),
  )
  expect(v.status).toBe('queued')
  expect(v.queue_position).toBe(1)
  expect(v.image).toBe('img:1')
  expect(v.env).toEqual({ REPO: 'https://x/r.git', PROMPT: 'p' })
  expect(v.cmd).toBeNull()
  expect(v.idle_timeout_s).toBe(1800)
  const raw = JSON.stringify(store.db.query('SELECT * FROM sessions').all())
  expect(raw).not.toContain('gt')
  expect(raw).not.toContain('GIT_TOKEN')
})

test('ownership: another owner sees 404; cancel and tokens', () => {
  const { svc } = setup()
  const v = svc.create(body({ owner_id: 'me', image: 'i' }))
  expect(svc.get(v.id, 'me').id).toBe(v.id)
  expect(() => svc.get(v.id, 'you')).toThrow(/session not found/)
  expect(() => svc.get(v.id, '')).toThrow(/owner_id is required/)
  expect(() => svc.attachToken(v.id, 'me')).toThrow(/session is queued/)
  const p = svc.previewToken(v.id, 'me', 3000)
  expect(p.url).toBe(`https://3000-${v.id}.preview.example.com/?t=${p.token}`)
  expect(() => svc.previewToken(v.id, 'me', 0)).toThrow(/port must be/)
  expect(svc.cancel(v.id, 'me').status).toBe('ended')
  expect(svc.list('me')).toHaveLength(1)
  expect(svc.list('you')).toHaveLength(0)
})

test('Env.validate: names, reserved keys, types', () => {
  expect(Env.validate('env', undefined)).toEqual({})
  expect(Env.validate('env', { A_1: 'x' })).toEqual({ A_1: 'x' })
  expect(() => Env.validate('env', { 'bad-name': 'x' })).toThrow(/invalid variable name/)
  expect(() => Env.validate('env', { TERM: 'x' })).toThrow(/reserved/)
  expect(() => Env.validate('env', { A: 1 })).toThrow(/must be a string/)
  expect(() => Env.validate('env', ['A'])).toThrow(/object/)
})
