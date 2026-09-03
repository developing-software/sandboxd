// Running sessions on this host: sandbox + PTY + ring buffer + idle timer.
import type { EndReason, SessionSpec, Size } from "../protocol/messages.ts";
import type { PtyStream, SandboxDriver } from "./driver.ts";
import { PtyFanout } from "./pty.ts";
import { logger } from "../shared/log.ts";

const log = logger("agent.sessions");

interface Running {
  spec: SessionSpec;
  containerId: string;
  pty: PtyStream;
  fanout: PtyFanout;
  size: Size;
  ending: boolean;
}

export interface SessionEvents {
  started(sid: string): void;
  ended(sid: string, reason: EndReason, detail?: string): void;
}

const NO_EVENTS: SessionEvents = { started() {}, ended() {} };
const INITIAL_SIZE: Size = { cols: 120, rows: 40 };

export class SessionManager {
  private sessions = new Map<string, Running>();
  private timer: ReturnType<typeof setInterval>;
  private events: SessionEvents = NO_EVENTS;

  constructor(private driver: SandboxDriver, private entry: string[]) {
    this.timer = setInterval(() => this.reapIdle(), 15_000);
  }

  /** Subscribe after construction; the tunnel is built after the manager and needs it too. */
  on(events: SessionEvents) { this.events = events; }

  running(): string[] { return [...this.sessions.keys()]; }
  count() { return this.sessions.size; }
  has(sid: string) { return this.sessions.has(sid); }

  async create(spec: SessionSpec) {
    if (this.sessions.has(spec.sid)) return;
    let containerId: string | undefined;
    try {
      containerId = await this.driver.create({ sid: spec.sid, image: spec.image });
      const env = { ...spec.env, ...spec.secret_env, TERM: "xterm-256color", DEVAGENTS_SESSION_ID: spec.sid };
      const size = { ...INITIAL_SIZE };
      const pty = await this.driver.attach(containerId, spec.cmd ?? this.entry, env, size);
      const fanout = new PtyFanout();
      const run: Running = { spec, containerId, pty, fanout, size, ending: false };
      this.sessions.set(spec.sid, run);
      pty.onData((d) => fanout.emit(d));
      pty.onExit(() => { void this.end(spec.sid, "exited"); });
      // Secrets were handed to docker exec; drop our copy.
      spec.secret_env = {};
      this.events.started(spec.sid);
      log.info("session started", { sid: spec.sid, container: containerId.slice(0, 12) });
    } catch (e) {
      log.error("session create failed", { sid: spec.sid, err: String(e) });
      if (containerId) await this.driver.destroy(containerId).catch(() => {});
      this.events.ended(spec.sid, "failed", String(e));
    }
  }

  attachViewer(sid: string, size: Size, sub: (d: Uint8Array) => void): { replay: Uint8Array; unsubscribe: () => void } | null {
    const run = this.sessions.get(sid);
    if (!run) return null;
    const unsubscribe = run.fanout.subscribe(sub);
    this.resize(sid, size);
    return { replay: run.fanout.buffer.snapshot(), unsubscribe };
  }

  write(sid: string, data: Uint8Array) {
    const run = this.sessions.get(sid);
    if (!run) return;
    run.fanout.touch();
    run.pty.write(data);
  }

  resize(sid: string, size: Size) {
    const run = this.sessions.get(sid);
    if (!run || (run.size.cols === size.cols && run.size.rows === size.rows)) return;
    run.size = size; // last resize wins
    void run.pty.resize(size);
  }

  containerOf(sid: string): string | undefined { return this.sessions.get(sid)?.containerId; }

  async end(sid: string, reason: EndReason, detail?: string) {
    const run = this.sessions.get(sid);
    if (!run || run.ending) return;
    run.ending = true;
    this.sessions.delete(sid);
    try { run.pty.close(); } catch {}
    await this.driver.destroy(run.containerId).catch((e) => log.warn("destroy failed", { sid, err: String(e) }));
    log.info("session ended", { sid, reason });
    this.events.ended(sid, reason, detail);
  }

  private reapIdle() {
    const now = Date.now();
    for (const [sid, run] of this.sessions) {
      if (now - run.fanout.lastActivity > run.spec.idle_timeout_s * 1000) void this.end(sid, "idle");
    }
  }

  async endAll(reason: EndReason) {
    clearInterval(this.timer);
    await Promise.all(this.running().map((sid) => this.end(sid, reason)));
  }
}
