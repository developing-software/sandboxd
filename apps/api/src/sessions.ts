// Session use cases behind the HTTP API. The body has already been shaped by
// `CreateSession`; what is left is the semantics: merging sidecar sources, splitting
// secrets, and ownership (the CP has no user table, `owner_id` is the whole model).
import type { CpConfig } from './config'
import type { Store, Session } from './store'
import type { Scheduler } from './scheduler'
import type { Tokens } from './tokens'
import { badRequest, conflict, notFound } from '@sandboxd/core/errors'
import { newSessionId } from '@sandboxd/core/ids'
import { fromDecls, mergeServices, sandboxEnvOf } from './services'
import { parseCompose } from './compose'
import { DEFAULT_IDLE_S, type CreateSession, type SessionView } from './schema'

const ATTACH_TTL_MS = 60_000
const PREVIEW_TTL_MS = 10 * 60_000

export interface HostOnline {
  isOnline(hostId: string): boolean
}

export class SessionService {
  constructor(
    private cfg: Pick<CpConfig, 'publicUrl' | 'previewDomain' | 'maxServices'>,
    private store: Store,
    private hosts: HostOnline,
    private sched: Scheduler,
    private tokens: Tokens,
  ) {}

  create(b: CreateSession): SessionView {
    const id = newSessionId()
    // Sidecars from both sources, in start order: the caller's list, then its compose file.
    const services = mergeServices(
      [fromDecls(b.services ?? []), parseCompose(b.compose)],
      this.cfg.maxServices,
    )
    // Precedence: caller env > what services ask to inject (DATABASE_URL and friends).
    const env = { ...sandboxEnvOf(services), ...b.env }
    const row = this.store.insertSession({
      id,
      owner_id: b.owner_id,
      image: b.image,
      cmd: b.cmd ?? null,
      env,
      services: services.decls,
      idle_timeout_s: b.idle_timeout_s ?? DEFAULT_IDLE_S,
      created_at: Date.now(),
    })
    this.sched.submit(row, { ...b.secret_env }, services.secrets)
    return this.view(this.store.session(id)!)
  }

  list(owner_id?: string): SessionView[] {
    return this.store.listSessions(owner_id).map((s) => this.view(s))
  }

  get(sid: string, owner_id: string): SessionView {
    return this.view(this.owned(sid, owner_id))
  }

  cancel(sid: string, owner_id: string): SessionView {
    const s = this.owned(sid, owner_id)
    this.sched.cancel(s)
    return this.view(this.store.session(s.id)!)
  }

  attachToken(sid: string, owner_id: string) {
    const s = this.owned(sid, owner_id)
    if (s.status !== 'running') throw conflict(`session is ${s.status}`)
    const token = this.tokens.sign({ k: 'attach', sid: s.id }, ATTACH_TTL_MS)
    const ws = this.cfg.publicUrl.replace(/^http/, 'ws')
    return {
      token,
      wss_url: `${ws}/attach?token=${token}`,
      expires_in_s: ATTACH_TTL_MS / 1000,
    }
  }

  previewToken(sid: string, owner_id: string, port: number) {
    const s = this.owned(sid, owner_id)
    if (!Number.isInteger(port) || port < 1 || port > 65535)
      throw badRequest('port must be 1-65535')
    const token = this.tokens.sign({ k: 'preview', sid: s.id, port }, PREVIEW_TTL_MS)
    const pub = new URL(this.cfg.publicUrl)
    const portSuffix = pub.port ? `:${pub.port}` : ''
    const url = `${pub.protocol}//${port}-${s.id}.${this.cfg.previewDomain}${portSuffix}/?t=${token}`
    return { token, url, expires_in_s: PREVIEW_TTL_MS / 1000 }
  }

  /** Ownership check. A session of another owner is indistinguishable from a missing one. */
  private owned(sid: string, owner_id: string): Session {
    if (!owner_id) throw badRequest('owner_id is required')
    const s = this.store.session(sid)
    if (!s || s.owner_id !== owner_id) throw notFound('session')
    return s
  }

  private view(s: Session): SessionView {
    return {
      id: s.id,
      owner_id: s.owner_id,
      status: s.status,
      host_id: s.host_id,
      host_online: s.host_id ? this.hosts.isOnline(s.host_id) : null,
      queue_position: s.status === 'queued' ? this.sched.queuePosition(s.id) : null,
      ended_reason: s.ended_reason,
      ended_detail: s.ended_detail,
      image: s.image,
      cmd: s.cmd,
      env: s.env,
      services: s.services,
      idle_timeout_s: s.idle_timeout_s,
      created_at: s.created_at,
      started_at: s.started_at,
      ended_at: s.ended_at,
    }
  }
}
