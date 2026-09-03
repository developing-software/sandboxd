// Running sessions on this host. A session is a pod: optional private network,
// sidecar services started first (with readiness probes), then the sandbox with
// its PTY, ring buffer and idle timer. Everything is torn down together.
import type { EndReason, ServiceSpec, SessionSpec, Size } from "../protocol/messages.ts";
import type { PtyStream, SandboxDriver } from "./driver.ts";
import { PtyFanout } from "./pty.ts";
import { logger } from "../shared/log.ts";

const log = logger("agent.sessions");

/** The sandbox's alias on the session network, so services can reach it too. */
const SANDBOX_ALIAS = "sandbox";
const READY_POLL_MS = 500;

interface Running {
  spec: SessionSpec;
  containerId: string;
  /** Sidecar container ids, in start order. */
  services: string[];
  network: string | null;
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

  constructor(private driver: SandboxDriver, private entry: string[], private sleep = (ms: number) => Bun.sleep(ms)) {
    this.timer = setInterval(() => this.reapIdle(), 15_000);
  }

  /** Subscribe after construction; the tunnel is built after the manager and needs it too. */
  on(events: SessionEvents) { this.events = events; }

  running(): string[] { return [...this.sessions.keys()]; }
  count() { return this.sessions.size; }
  has(sid: string) { return this.sessions.has(sid); }

  async create(spec: SessionSpec) {
    if (this.sessions.has(spec.sid)) return;
    const sid = spec.sid;
    const services: string[] = [];
    let network: string | null = null;
    let containerId: string | undefined;
    try {
      if (spec.services.length) network = await this.driver.createNetwork(sid);
      for (const svc of spec.services) {
        const id = await this.driver.create({
          sid, image: svc.image, role: "service", network: network!, alias: svc.name, env: { ...svc.env, ...svc.secret_env },
          ...(svc.cmd ? { cmd: svc.cmd } : {}),
        });
        services.push(id);
        if (svc.ready) await this.waitReady(id, svc);
        log.info("service up", { sid, service: svc.name });
      }
      containerId = await this.driver.create({ sid, image: spec.image, role: "sandbox", ...(network ? { network, alias: SANDBOX_ALIAS } : {}) });
      const env = { ...spec.env, ...spec.secret_env, TERM: "xterm-256color", DEVAGENTS_SESSION_ID: sid };
      const size = { ...INITIAL_SIZE };
      const pty = await this.driver.attach(containerId, spec.cmd ?? this.entry, env, size);
      const fanout = new PtyFanout();
      const run: Running = { spec, containerId, services, network, pty, fanout, size, ending: false };
      this.sessions.set(sid, run);
      pty.onData((d) => fanout.emit(d));
      pty.onExit(() => { void this.end(sid, "exited"); });
      // Secrets were handed to docker; drop our copies.
      spec.secret_env = {};
      for (const svc of spec.services) svc.secret_env = {};
      this.events.started(sid);
      log.info("session started", { sid, container: containerId.slice(0, 12), services: spec.services.length });
    } catch (e) {
      log.error("session create failed", { sid, err: String(e) });
      await this.teardown(sid, containerId, services, network);
      this.events.ended(sid, "failed", String(e));
    }
  }

  /** Poll `dial` until the service accepts a TCP connection, or throw after its timeout. */
  private async waitReady(id: string, svc: ServiceSpec) {
    const { port, timeout_s } = svc.ready!;
    const deadline = Date.now() + timeout_s * 1000;
    let lastErr = "";
    while (Date.now() < deadline) {
      try {
        const d = await this.driver.dial(id, port);
        d.end();
        return;
      } catch (e) {
        lastErr = String(e);
        await this.sleep(READY_POLL_MS);
      }
    }
    throw new Error(`service ${svc.name} not ready on port ${port} after ${timeout_s}s: ${lastErr}`);
  }

  /** Remove the sandbox, then the services in reverse start order, then the network. Best effort. */
  private async teardown(sid: string, containerId: string | undefined, services: string[], network: string | null) {
    const rm = (id: string) => this.driver.destroy(id).catch((e) => log.warn("destroy failed", { sid, id: id.slice(0, 12), err: String(e) }));
    if (containerId) await rm(containerId);
    for (const id of [...services].reverse()) await rm(id);
    if (network) await this.driver.removeNetwork(sid).catch((e) => log.warn("network remove failed", { sid, err: String(e) }));
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
    await this.teardown(sid, run.containerId, run.services, run.network);
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
