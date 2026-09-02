// Placement + FIFO queue + reconciliation. Secrets for queued sessions live
// only here, in memory; they are never written to the store.
import type { EndReason, SessionSpec } from "../protocol/messages.ts";
import type { Store, SessionRow } from "./store.ts";
import type { HostHub } from "./tunnel.ts";
import { logger } from "../shared/log.ts";

const log = logger("sched");
const UNKNOWN_GRACE_MS = 90_000;

export class Scheduler {
  private secrets = new Map<string, Record<string, string>>();
  private timer: ReturnType<typeof setInterval>;

  /** `sandboxEnv` is operator-level env merged under every session's env at placement; never persisted. */
  constructor(private store: Store, private hub: HostHub, private sandboxEnv: Record<string, string> = {}) {
    this.timer = setInterval(() => this.reapUnknown(), 15_000);
  }

  /** On CP boot: running sessions become unknown until their host re-reports;
   *  queued sessions lost their in-memory secrets and cannot be placed. */
  boot() {
    this.store.markActiveUnknown();
    for (const s of this.store.queuedSessions()) {
      this.store.markEnded(s.id, "failed", "control plane restarted before placement; secrets are not persisted");
    }
  }

  submit(row: SessionRow, secret_env: Record<string, string>) {
    this.secrets.set(row.id, secret_env);
    if (!this.place(row)) log.info("queued", { sid: row.id, position: this.queuePosition(row.id) });
  }

  queuePosition(sid: string): number | null {
    const idx = this.store.queuedSessions().findIndex((s) => s.id === sid);
    return idx < 0 ? null : idx + 1;
  }

  drain() {
    for (const row of this.store.queuedSessions()) if (!this.place(row)) break;
  }

  private pickHost(): string | null {
    let best: { id: string; free: number } | null = null;
    for (const h of this.store.listHosts()) {
      if (h.status !== "approved") continue;
      const cap = this.hub.capacity(h.id);
      if (!cap) continue;
      const free = cap.max - cap.running;
      if (free <= 0) continue;
      if (!best || free > best.free) best = { id: h.id, free };
    }
    return best?.id ?? null;
  }

  private place(row: SessionRow): boolean {
    const hostId = this.pickHost();
    if (!hostId) return false;
    const env: Record<string, string> = JSON.parse(row.env);
    // Precedence: session secret_env > session env > operator sandboxEnv.
    // Operator env travels as secret_env because it may hold keys (e.g. LLM_API_KEY) and is never persisted.
    const secret_env: Record<string, string> = { ...this.sandboxEnv };
    for (const k of Object.keys(env)) delete secret_env[k];
    Object.assign(secret_env, this.secrets.get(row.id) ?? {});
    const spec: SessionSpec = {
      sid: row.id, image: row.image, cmd: row.cmd ? JSON.parse(row.cmd) : null,
      idle_timeout_s: row.idle_timeout_s, env, secret_env,
    };
    if (!this.hub.createSession(hostId, spec)) return false;
    this.secrets.delete(row.id);
    this.store.markCreating(row.id, hostId);
    log.info("placed", { sid: row.id, hostId });
    return true;
  }

  cancel(row: SessionRow) {
    this.secrets.delete(row.id);
    if (row.status === "queued") { this.store.markEnded(row.id, "closed"); return; }
    if (row.host_id) this.hub.destroySession(row.host_id, row.id);
    if (!row.host_id || !this.hub.isOnline(row.host_id)) this.store.markEnded(row.id, "closed", "host offline at close");
  }

  // ---- hub events ----
  onHostOnline(hostId: string, running: string[]) {
    const known = new Set(running);
    for (const s of this.store.activeSessionsOnHost(hostId)) {
      if (known.has(s.id)) this.store.markKnown(s.id);
      else this.store.markEnded(s.id, "lost", "host reconnected without this session");
    }
    this.drain();
  }
  onHeartbeat() { this.drain(); }
  onSessionStarted(sid: string) { this.store.markRunning(sid); }
  onSessionEnded(sid: string, reason: EndReason, detail?: string) {
    this.secrets.delete(sid);
    this.store.markEnded(sid, reason, detail);
    this.drain();
  }

  private reapUnknown() {
    const cutoff = Date.now() - UNKNOWN_GRACE_MS;
    for (const s of this.store.activeSessions()) {
      if (s.unknown_since !== null && s.unknown_since < cutoff) {
        this.store.markEnded(s.id, "lost", "host did not report this session after control plane restart");
      }
    }
  }
}
