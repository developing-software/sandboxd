// Placement + FIFO queue + reconciliation. Secrets for queued sessions live
// only here, in memory; they are never written to the store.
import type { EndReason, ServiceSpec, SessionSpec } from "../protocol/messages.ts";
import type { Store, Session } from "./store.ts";
import type { HostPlacement, HubEvents } from "./hosts/hub.ts";
import { logger } from "../shared/log.ts";

const log = logger("sched");
const UNKNOWN_GRACE_MS = 90_000;

/** Secrets held in memory for a queued session, keyed by service name for sidecars. */
interface SessionSecrets { env: Record<string, string>; services: Record<string, Record<string, string>> }

export interface Candidate { hostId: string; free: number }

/** Placement policy: choose one host among those with free slots, or null to queue. */
export interface Placement { pick(candidates: Candidate[]): string | null }

/** Design decision 8: the host with the most free slots wins. */
export const mostFreeSlots: Placement = {
  pick(candidates) {
    let best: Candidate | null = null;
    for (const c of candidates) if (!best || c.free > best.free) best = c;
    return best?.hostId ?? null;
  },
};

export class Scheduler implements HubEvents {
  private secrets = new Map<string, SessionSecrets>();
  private timer: ReturnType<typeof setInterval>;

  /** `sandboxEnv` is operator-level env merged under every session's env at placement; never persisted. */
  constructor(
    private store: Store, private hub: HostPlacement,
    private sandboxEnv: Record<string, string> = {}, private placement: Placement = mostFreeSlots,
  ) {
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

  /** `serviceSecrets` is keyed by service name. Both maps are memory-only until placement. */
  submit(s: Session, secret_env: Record<string, string>, serviceSecrets: Record<string, Record<string, string>> = {}) {
    this.secrets.set(s.id, { env: secret_env, services: serviceSecrets });
    if (!this.place(s)) log.info("queued", { sid: s.id, position: this.queuePosition(s.id) });
  }

  queuePosition(sid: string): number | null {
    const idx = this.store.queuedSessions().findIndex((s) => s.id === sid);
    return idx < 0 ? null : idx + 1;
  }

  drain() {
    for (const s of this.store.queuedSessions()) if (!this.place(s)) break;
  }

  private candidates(): Candidate[] {
    const out: Candidate[] = [];
    for (const h of this.store.listHosts()) {
      if (h.status !== "approved") continue;
      const cap = this.hub.capacity(h.id);
      if (!cap) continue;
      const free = cap.max - cap.running;
      if (free > 0) out.push({ hostId: h.id, free });
    }
    return out;
  }

  private place(s: Session): boolean {
    const hostId = this.placement.pick(this.candidates());
    if (!hostId) return false;
    // Precedence: session secret_env > session env > operator sandboxEnv.
    // Operator env travels as secret_env because it may hold keys (e.g. LLM_API_KEY) and is never persisted.
    const secret_env: Record<string, string> = { ...this.sandboxEnv };
    for (const k of Object.keys(s.env)) delete secret_env[k];
    const held = this.secrets.get(s.id);
    Object.assign(secret_env, held?.env ?? {});
    const services: ServiceSpec[] = s.services.map((d) => ({ ...d, env: { ...d.env }, secret_env: { ...(held?.services[d.name] ?? {}) } }));
    const spec: SessionSpec = { sid: s.id, image: s.image, cmd: s.cmd, idle_timeout_s: s.idle_timeout_s, env: { ...s.env }, secret_env, services };
    if (!this.hub.createSession(hostId, spec)) return false;
    this.secrets.delete(s.id);
    this.store.markCreating(s.id, hostId);
    log.info("placed", { sid: s.id, hostId });
    return true;
  }

  cancel(s: Session) {
    this.secrets.delete(s.id);
    if (s.status === "queued") { this.store.markEnded(s.id, "closed"); return; }
    if (s.host_id) this.hub.destroySession(s.host_id, s.id);
    if (!s.host_id || !this.hub.isOnline(s.host_id)) this.store.markEnded(s.id, "closed", "host offline at close");
  }

  // ---- HubEvents ----
  hostOnline(hostId: string, running: string[]) {
    const known = new Set(running);
    for (const s of this.store.activeSessionsOnHost(hostId)) {
      if (known.has(s.id)) this.store.markKnown(s.id);
      else this.store.markEnded(s.id, "lost", "host reconnected without this session");
    }
    this.drain();
  }
  heartbeat() { this.drain(); }
  sessionStarted(sid: string) { this.store.markRunning(sid); }
  sessionEnded(sid: string, reason: EndReason, detail?: string) {
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
