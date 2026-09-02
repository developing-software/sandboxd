import { Database } from "bun:sqlite";
import { mkdirSync } from "node:fs";
import { dirname } from "node:path";
import type { EndReason } from "../protocol/messages.ts";

export type HostStatus = "pending" | "approved" | "revoked";
export type SessionStatus = "queued" | "creating" | "running" | "ended";

export interface HostRow {
  id: string; name: string; fingerprint: string; status: HostStatus;
  approve_code: string | null; max_sessions: number;
  last_seen_at: number | null; created_at: number;
}

export interface SessionRow {
  id: string; owner_id: string; host_id: string | null; status: SessionStatus;
  ended_reason: EndReason | null; ended_detail: string | null;
  repo: string; branch: string; base_branch: string | null; prompt: string;
  image: string; idle_timeout_s: number;
  created_at: number; started_at: number | null; ended_at: number | null;
  /** Set on CP boot for sessions whose host hasn't re-reported them yet. */
  unknown_since: number | null;
}

export class Store {
  readonly db: Database;

  constructor(path = ":memory:") {
    if (path !== ":memory:") mkdirSync(dirname(path), { recursive: true });
    this.db = new Database(path, { create: true });
    this.db.exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;");
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS hosts (
        id TEXT PRIMARY KEY, name TEXT NOT NULL, fingerprint TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL, approve_code TEXT, max_sessions INTEGER NOT NULL DEFAULT 0,
        last_seen_at INTEGER, created_at INTEGER NOT NULL
      );
      CREATE TABLE IF NOT EXISTS sessions (
        id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, host_id TEXT REFERENCES hosts(id),
        status TEXT NOT NULL, ended_reason TEXT, ended_detail TEXT,
        repo TEXT NOT NULL, branch TEXT NOT NULL, base_branch TEXT, prompt TEXT NOT NULL,
        image TEXT NOT NULL, idle_timeout_s INTEGER NOT NULL,
        created_at INTEGER NOT NULL, started_at INTEGER, ended_at INTEGER, unknown_since INTEGER
      );
      CREATE INDEX IF NOT EXISTS sessions_owner ON sessions(owner_id, created_at);
      CREATE INDEX IF NOT EXISTS sessions_status ON sessions(status, created_at);
    `);
  }

  // ---- hosts ----
  listHosts(): HostRow[] { return this.db.query<HostRow, []>("SELECT * FROM hosts ORDER BY created_at").all(); }
  hostById(id: string): HostRow | null { return this.db.query<HostRow, [string]>("SELECT * FROM hosts WHERE id=?").get(id); }
  hostByFingerprint(fp: string): HostRow | null { return this.db.query<HostRow, [string]>("SELECT * FROM hosts WHERE fingerprint=?").get(fp); }

  insertPendingHost(h: { id: string; name: string; fingerprint: string; approve_code: string; max_sessions: number }): HostRow {
    this.db.run(
      "INSERT INTO hosts (id,name,fingerprint,status,approve_code,max_sessions,last_seen_at,created_at) VALUES (?,?,?,'pending',?,?,?,?)",
      [h.id, h.name, h.fingerprint, h.approve_code, h.max_sessions, Date.now(), Date.now()],
    );
    return this.hostById(h.id)!;
  }
  approveHost(id: string) { this.db.run("UPDATE hosts SET status='approved', approve_code=NULL WHERE id=?", [id]); }
  revokeHost(id: string) { this.db.run("UPDATE hosts SET status='revoked' WHERE id=?", [id]); }
  touchHost(id: string, patch: { name?: string; max_sessions?: number }) {
    this.db.run("UPDATE hosts SET last_seen_at=?, name=COALESCE(?,name), max_sessions=COALESCE(?,max_sessions) WHERE id=?",
      [Date.now(), patch.name ?? null, patch.max_sessions ?? null, id]);
  }

  // ---- sessions ----
  insertSession(s: Omit<SessionRow, "host_id" | "status" | "ended_reason" | "ended_detail" | "started_at" | "ended_at" | "unknown_since">): SessionRow {
    this.db.run(
      `INSERT INTO sessions (id,owner_id,status,repo,branch,base_branch,prompt,image,idle_timeout_s,created_at)
       VALUES (?,?,'queued',?,?,?,?,?,?,?)`,
      [s.id, s.owner_id, s.repo, s.branch, s.base_branch, s.prompt, s.image, s.idle_timeout_s, s.created_at],
    );
    return this.session(s.id)!;
  }
  session(id: string): SessionRow | null { return this.db.query<SessionRow, [string]>("SELECT * FROM sessions WHERE id=?").get(id); }
  listSessions(owner_id?: string): SessionRow[] {
    return owner_id
      ? this.db.query<SessionRow, [string]>("SELECT * FROM sessions WHERE owner_id=? ORDER BY created_at DESC").all(owner_id)
      : this.db.query<SessionRow, []>("SELECT * FROM sessions ORDER BY created_at DESC").all();
  }
  queuedSessions(): SessionRow[] { return this.db.query<SessionRow, []>("SELECT * FROM sessions WHERE status='queued' ORDER BY created_at").all(); }
  activeSessionsOnHost(host_id: string): SessionRow[] {
    return this.db.query<SessionRow, [string]>("SELECT * FROM sessions WHERE host_id=? AND status IN ('creating','running')").all(host_id);
  }
  activeSessions(): SessionRow[] {
    return this.db.query<SessionRow, []>("SELECT * FROM sessions WHERE status IN ('creating','running')").all();
  }

  markCreating(id: string, host_id: string) { this.db.run("UPDATE sessions SET status='creating', host_id=? WHERE id=?", [host_id, id]); }
  markRunning(id: string) { this.db.run("UPDATE sessions SET status='running', started_at=?, unknown_since=NULL WHERE id=?", [Date.now(), id]); }
  markKnown(id: string) { this.db.run("UPDATE sessions SET unknown_since=NULL WHERE id=?", [id]); }
  markEnded(id: string, reason: EndReason, detail?: string) {
    this.db.run("UPDATE sessions SET status='ended', ended_reason=?, ended_detail=?, ended_at=?, unknown_since=NULL WHERE id=? AND status<>'ended'",
      [reason, detail ?? null, Date.now(), id]);
  }
  markActiveUnknown() { this.db.run("UPDATE sessions SET unknown_since=? WHERE status IN ('creating','running')", [Date.now()]); }
}
