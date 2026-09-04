import { Database } from 'bun:sqlite'
import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import type { Msg } from '@sandboxd/core/messages'
import { Log } from '@sandboxd/core/log'

const log = Log.create('store')

/** Bump when the schema changes. There are no migrations: a db with another version is wiped. */
const SCHEMA_VERSION = 5

export type HostStatus = 'pending' | 'approved' | 'revoked'
export type SessionStatus = 'queued' | 'creating' | 'running' | 'ended'

export interface HostRow {
  id: string
  name: string
  fingerprint: string
  status: HostStatus
  approve_code: string | null
  max_sessions: number
  last_seen_at: number | null
  created_at: number
}

/** A session as the rest of the CP sees it: JSON columns already decoded. */
export interface Session {
  id: string
  owner_id: string
  host_id: string | null
  status: SessionStatus
  ended_reason: Msg.EndReason | null
  ended_detail: string | null
  image: string
  /** null = the daemon's default entry. */
  cmd: string[] | null
  /** Non-secret env only. Secrets never reach the store. */
  env: Record<string, string>
  idle_timeout_s: number
  created_at: number
  started_at: number | null
  ended_at: number | null
  /** Set on CP boot for sessions whose host hasn't re-reported them yet. */
  unknown_since: number | null
}

export type NewSession = Pick<
  Session,
  'id' | 'owner_id' | 'image' | 'cmd' | 'env' | 'idle_timeout_s' | 'created_at'
>

/** Raw row shape: `cmd` and `env` are JSON text in SQLite. */
interface SessionRow extends Omit<Session, 'cmd' | 'env'> {
  cmd: string | null
  env: string
}

const toSession = (r: SessionRow): Session => ({
  ...r,
  cmd: r.cmd ? JSON.parse(r.cmd) : null,
  env: JSON.parse(r.env),
})

export class Store {
  readonly db: Database

  constructor(path = ':memory:') {
    if (path !== ':memory:') mkdirSync(dirname(path), { recursive: true })
    this.db = new Database(path, { create: true })
    this.db.exec('PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;')
    const version =
      this.db.query<{ user_version: number }, []>('PRAGMA user_version').get()?.user_version ??
      0
    const hasTables = !!this.db
      .query("SELECT 1 FROM sqlite_master WHERE type='table' AND name='sessions'")
      .get()
    // Every row is disposable (sessions are ephemeral, hosts re-enroll), so an old schema
    // is dropped rather than refused: a unit under Restart=always must not crash-loop.
    if (hasTables && version !== SCHEMA_VERSION) {
      log.warn('schema version changed; dropping all state', {
        path,
        found: version,
        expected: SCHEMA_VERSION,
      })
      this.db.exec('DROP TABLE IF EXISTS sessions; DROP TABLE IF EXISTS hosts;')
    }
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS hosts (
        id TEXT PRIMARY KEY, name TEXT NOT NULL, fingerprint TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL, approve_code TEXT, max_sessions INTEGER NOT NULL DEFAULT 0,
        last_seen_at INTEGER, created_at INTEGER NOT NULL
      );
      CREATE TABLE IF NOT EXISTS sessions (
        id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, host_id TEXT REFERENCES hosts(id),
        status TEXT NOT NULL, ended_reason TEXT, ended_detail TEXT,
        image TEXT NOT NULL, cmd TEXT, env TEXT NOT NULL, idle_timeout_s INTEGER NOT NULL,
        created_at INTEGER NOT NULL, started_at INTEGER, ended_at INTEGER, unknown_since INTEGER
      );
      CREATE INDEX IF NOT EXISTS sessions_owner ON sessions(owner_id, created_at);
      CREATE INDEX IF NOT EXISTS sessions_status ON sessions(status, created_at);
    `)
    this.db.exec(`PRAGMA user_version=${SCHEMA_VERSION}`)
  }

  // ---- hosts ----
  listHosts(): HostRow[] {
    return this.db.query<HostRow, []>('SELECT * FROM hosts ORDER BY created_at').all()
  }
  hostById(id: string): HostRow | null {
    return this.db.query<HostRow, [string]>('SELECT * FROM hosts WHERE id=?').get(id)
  }
  hostByFingerprint(fp: string): HostRow | null {
    return this.db.query<HostRow, [string]>('SELECT * FROM hosts WHERE fingerprint=?').get(fp)
  }

  insertPendingHost(h: {
    id: string
    name: string
    fingerprint: string
    approve_code: string
    max_sessions: number
  }): HostRow {
    this.db.run(
      "INSERT INTO hosts (id,name,fingerprint,status,approve_code,max_sessions,last_seen_at,created_at) VALUES (?,?,?,'pending',?,?,?,?)",
      [h.id, h.name, h.fingerprint, h.approve_code, h.max_sessions, Date.now(), Date.now()],
    )
    return this.hostById(h.id)!
  }
  approveHost(id: string) {
    this.db.run("UPDATE hosts SET status='approved', approve_code=NULL WHERE id=?", [id])
  }
  revokeHost(id: string) {
    this.db.run("UPDATE hosts SET status='revoked' WHERE id=?", [id])
  }
  touchHost(id: string, patch: { name?: string; max_sessions?: number }) {
    this.db.run(
      'UPDATE hosts SET last_seen_at=?, name=COALESCE(?,name), max_sessions=COALESCE(?,max_sessions) WHERE id=?',
      [Date.now(), patch.name ?? null, patch.max_sessions ?? null, id],
    )
  }

  // ---- sessions ----
  private rows(sql: string, params: string[] = []): Session[] {
    return this.db
      .query<SessionRow, string[]>(sql)
      .all(...params)
      .map(toSession)
  }

  insertSession(s: NewSession): Session {
    this.db.run(
      `INSERT INTO sessions (id,owner_id,status,image,cmd,env,idle_timeout_s,created_at)
       VALUES (?,?,'queued',?,?,?,?,?)`,
      [
        s.id,
        s.owner_id,
        s.image,
        s.cmd ? JSON.stringify(s.cmd) : null,
        JSON.stringify(s.env),
        s.idle_timeout_s,
        s.created_at,
      ],
    )
    return this.session(s.id)!
  }
  session(id: string): Session | null {
    const r = this.db.query<SessionRow, [string]>('SELECT * FROM sessions WHERE id=?').get(id)
    return r ? toSession(r) : null
  }
  listSessions(owner_id?: string): Session[] {
    return owner_id
      ? this.rows('SELECT * FROM sessions WHERE owner_id=? ORDER BY created_at DESC', [
          owner_id,
        ])
      : this.rows('SELECT * FROM sessions ORDER BY created_at DESC')
  }
  queuedSessions(): Session[] {
    return this.rows("SELECT * FROM sessions WHERE status='queued' ORDER BY created_at")
  }
  activeSessionsOnHost(host_id: string): Session[] {
    return this.rows(
      "SELECT * FROM sessions WHERE host_id=? AND status IN ('creating','running')",
      [host_id],
    )
  }
  activeSessions(): Session[] {
    return this.rows("SELECT * FROM sessions WHERE status IN ('creating','running')")
  }

  markCreating(id: string, host_id: string) {
    this.db.run("UPDATE sessions SET status='creating', host_id=? WHERE id=?", [host_id, id])
  }
  markRunning(id: string) {
    this.db.run(
      "UPDATE sessions SET status='running', started_at=?, unknown_since=NULL WHERE id=?",
      [Date.now(), id],
    )
  }
  markKnown(id: string) {
    this.db.run('UPDATE sessions SET unknown_since=NULL WHERE id=?', [id])
  }
  markEnded(id: string, reason: Msg.EndReason, detail?: string) {
    this.db.run(
      "UPDATE sessions SET status='ended', ended_reason=?, ended_detail=?, ended_at=?, unknown_since=NULL WHERE id=? AND status<>'ended'",
      [reason, detail ?? null, Date.now(), id],
    )
  }
  markActiveUnknown() {
    this.db.run("UPDATE sessions SET unknown_since=? WHERE status IN ('creating','running')", [
      Date.now(),
    ])
  }
}
