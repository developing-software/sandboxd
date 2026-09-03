// Session use cases behind the HTTP API. Auth has already happened; every call
// carries owner_id and ownership is enforced here (the CP has no user table).
import type { CpConfig } from './config'
import type { Store, Session, ServiceDecl } from './store'
import type { Scheduler } from './scheduler'
import type { Tokens } from './tokens'
import type { PresetRegistry } from './presets/index'
import { badRequest, conflict, notFound } from './errors'
import { newSessionId } from '@sandboxd/core/ids'
import { validateEnv } from './env'
import {
  EMPTY_CATALOG,
  mergeServices,
  noServices,
  sandboxEnvOf,
  validateServices,
  type ServiceCatalog,
} from './services'
import { parseCompose } from './compose'

export { validateEnv } from './env'

const ATTACH_TTL_MS = 60_000
const PREVIEW_TTL_MS = 10 * 60_000
const DEFAULT_IDLE_S = 1800
const MIN_IDLE_S = 60

/** Core fields are generic; the resolved preset reads its own extra fields from the same body. */
export interface CreateSessionBody {
  owner_id: string
  preset?: string
  image?: string
  idle_timeout_s?: number
  /** Command exec'd in the PTY. Omit for the image's default entry. */
  cmd?: string[]
  /** Extra env (non-secret, persisted) merged over what the preset produced. */
  env?: Record<string, string>
  /** Extra secret env (never persisted) merged over what the preset produced. */
  secret_env?: Record<string, string>
  /** Sidecars on the session's private network: catalog names (`"postgres"`, see GET /services),
   *  `{use, name?, env?}` references, or full `{name, image, ...}` declarations. See services.ts. */
  services?: unknown
  /** A docker compose document (YAML text or object); its services become sidecars. See compose.ts. */
  compose?: unknown
  /** Preset-specific fields; see GET /presets for each preset's schema. */
  [presetField: string]: unknown
}

export interface SessionView {
  id: string
  owner_id: string
  status: Session['status']
  host_id: string | null
  host_online: boolean | null
  queue_position: number | null
  ended_reason: Session['ended_reason']
  ended_detail: string | null
  preset: string
  image: string
  cmd: string[] | null
  env: Record<string, string>
  services: ServiceDecl[]
  idle_timeout_s: number
  created_at: number
  started_at: number | null
  ended_at: number | null
}

export interface HostOnline {
  isOnline(hostId: string): boolean
}

export class SessionService {
  constructor(
    private cfg: Pick<
      CpConfig,
      'defaultImage' | 'publicUrl' | 'previewDomain' | 'maxServices'
    >,
    private store: Store,
    private hosts: HostOnline,
    private sched: Scheduler,
    private tokens: Tokens,
    private presets: PresetRegistry,
    private catalog: ServiceCatalog = EMPTY_CATALOG,
  ) {}

  create(raw: unknown): SessionView {
    if (!raw || typeof raw !== 'object' || Array.isArray(raw))
      throw badRequest('body must be a JSON object')
    const b = raw as CreateSessionBody
    if (!b.owner_id || typeof b.owner_id !== 'string') throw badRequest('owner_id is required')
    if (
      b.cmd !== undefined &&
      (!Array.isArray(b.cmd) ||
        b.cmd.length === 0 ||
        !b.cmd.every((c) => typeof c === 'string'))
    ) {
      throw badRequest('cmd must be a non-empty array of strings')
    }
    const id = newSessionId()
    const preset = this.presets.resolve(b)
    const expanded = preset.expand(id, b)
    const idle = Number(b.idle_timeout_s ?? expanded.idle_timeout_s ?? DEFAULT_IDLE_S)
    if (!Number.isFinite(idle) || idle < MIN_IDLE_S)
      throw badRequest(`idle_timeout_s must be >= ${MIN_IDLE_S}`)
    // Sidecars from every source, in start order: the preset's defaults, then the caller's list, then its compose file.
    const services = mergeServices(
      [
        expanded.services ?? noServices(),
        validateServices(b.services, this.catalog),
        parseCompose(b.compose),
      ],
      this.cfg.maxServices,
    )
    // Precedence: caller env > preset env > what services ask to inject (DATABASE_URL and friends).
    const env = { ...sandboxEnvOf(services), ...expanded.env, ...validateEnv('env', b.env) }
    const secret_env = { ...expanded.secret_env, ...validateEnv('secret_env', b.secret_env) }
    const row = this.store.insertSession({
      id,
      owner_id: b.owner_id,
      preset: preset.name,
      image:
        (typeof b.image === 'string' && b.image.trim()) ||
        expanded.image ||
        this.cfg.defaultImage,
      cmd: b.cmd ?? expanded.cmd ?? null,
      env,
      services: services.decls,
      idle_timeout_s: idle,
      created_at: Date.now(),
    })
    this.sched.submit(row, secret_env, services.secrets)
    return this.view(this.store.session(id)!)
  }

  list(owner_id?: string): SessionView[] {
    return this.store.listSessions(owner_id).map((s) => this.view(s))
  }

  get(sid: string, owner_id: string | null): SessionView {
    return this.view(this.owned(sid, owner_id))
  }

  cancel(sid: string, owner_id: string | null): SessionView {
    const s = this.owned(sid, owner_id)
    this.sched.cancel(s)
    return this.view(this.store.session(s.id)!)
  }

  attachToken(sid: string, owner_id: string | null) {
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

  previewToken(sid: string, owner_id: string | null, port: unknown) {
    const s = this.owned(sid, owner_id)
    const p = Number(port)
    if (!Number.isInteger(p) || p < 1 || p > 65535) throw badRequest('port must be 1-65535')
    const token = this.tokens.sign({ k: 'preview', sid: s.id, port: p }, PREVIEW_TTL_MS)
    const pub = new URL(this.cfg.publicUrl)
    const portSuffix = pub.port ? `:${pub.port}` : ''
    const url = `${pub.protocol}//${p}-${s.id}.${this.cfg.previewDomain}${portSuffix}/?t=${token}`
    return { token, url, expires_in_s: PREVIEW_TTL_MS / 1000 }
  }

  /** Ownership check. A session of another owner is indistinguishable from a missing one. */
  private owned(sid: string, owner_id: string | null): Session {
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
      preset: s.preset,
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
