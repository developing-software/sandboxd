// Host hub: owns every daemon WebSocket, does enrollment, and routes
// multiplexed PTY / port streams to their in-process subscribers.
import type { ServerWebSocket } from "bun";
import { encodeFrame, decodeFrame } from "../protocol/framing.ts";
import { parseMsg, type CpMsg, type HostMsg, type SessionSpec, type Size, type EndReason } from "../protocol/messages.ts";
import type { Store } from "./store.ts";
import { newApproveCode, newHostId } from "../shared/ids.ts";
import { logger } from "../shared/log.ts";

const log = logger("hub");

export interface TunnelData { kind: "tunnel"; hostId: string | null }
type WS = ServerWebSocket<TunnelData>;

interface StreamSub {
  onData(d: Uint8Array): void;
  onReplay?(): void;
  onOpen?(): void;
  onError?(msg: string): void;
  onClose(): void;
}

interface Conn {
  ws: WS; hostId: string; running: number; max: number;
  streams: Map<number, StreamSub>; nextStream: number;
}

export interface HubEvents {
  hostOnline(hostId: string, running: string[]): void;
  heartbeat(hostId: string): void;
  sessionStarted(sid: string): void;
  sessionEnded(sid: string, reason: EndReason, detail?: string): void;
}

export interface PtyHandle { write(d: Uint8Array): void; resize(size: Size): void; close(): void }
export interface PortHandle { write(d: Uint8Array): void; close(): void }

export class HostHub {
  private conns = new Map<string, Conn>();

  constructor(private store: Store, private events: HubEvents) {}

  isOnline(hostId: string) { return this.conns.has(hostId); }
  capacity(hostId: string): { running: number; max: number } | null {
    const c = this.conns.get(hostId);
    return c ? { running: c.running, max: c.max } : null;
  }

  private send(c: Conn, msg: CpMsg) { c.ws.send(JSON.stringify(msg)); }

  onOpen(ws: WS) { log.info("agent connected", { from: ws.remoteAddress }); }

  onMessage(ws: WS, raw: string | Buffer) {
    if (typeof raw === "string") {
      const msg = parseMsg<HostMsg>(raw);
      if (msg.type === "hello") return this.onHello(ws, msg);
      const c = ws.data.hostId ? this.conns.get(ws.data.hostId) : undefined;
      if (!c) return; // not approved / not hello'd yet
      this.onControl(c, msg);
    } else {
      const c = ws.data.hostId ? this.conns.get(ws.data.hostId) : undefined;
      if (!c) return;
      const { stream, payload } = decodeFrame(new Uint8Array(raw));
      c.streams.get(stream)?.onData(payload);
    }
  }

  onClose(ws: WS) {
    const hostId = ws.data.hostId;
    if (!hostId) { log.info("agent disconnected before hello"); return; }
    const c = this.conns.get(hostId);
    if (c && c.ws === ws) {
      for (const s of c.streams.values()) s.onClose();
      this.conns.delete(hostId);
      log.warn("host offline", { hostId });
    }
    this.pending.delete(hostId);
  }

  // ---- enrollment ----
  private pending = new Map<string, WS>();

  private onHello(ws: WS, msg: Extract<HostMsg, { type: "hello" }>) {
    let host = this.store.hostByFingerprint(msg.fingerprint);
    if (!host) {
      host = this.store.insertPendingHost({
        id: newHostId(), name: msg.name, fingerprint: msg.fingerprint,
        approve_code: newApproveCode(), max_sessions: msg.max_sessions,
      });
      log.info("new host pending approval", { hostId: host.id, name: host.name, code: host.approve_code });
    }
    ws.data.hostId = host.id;
    this.store.touchHost(host.id, { name: msg.name, max_sessions: msg.max_sessions });

    if (host.status === "revoked") {
      log.warn("revoked host tried to connect", { hostId: host.id, name: host.name });
      ws.send(JSON.stringify({ type: "hello.rejected", reason: "revoked" } satisfies CpMsg));
      ws.close();
      return;
    }
    if (host.status === "pending") {
      log.info("host waiting for approval", { hostId: host.id, name: host.name, code: host.approve_code });
      this.pending.set(host.id, ws);
      ws.send(JSON.stringify({ type: "hello.pending", host_id: host.id, code: host.approve_code! } satisfies CpMsg));
      return;
    }
    this.accept(ws, host.id, msg.running, msg.max_sessions);
  }

  private accept(ws: WS, hostId: string, running: string[], max: number) {
    const old = this.conns.get(hostId);
    if (old && old.ws !== ws) { for (const s of old.streams.values()) s.onClose(); old.ws.close(); }
    const c: Conn = { ws, hostId, running: running.length, max, streams: new Map(), nextStream: 1 };
    this.conns.set(hostId, c);
    this.pending.delete(hostId);
    this.send(c, { type: "hello.ok", host_id: hostId });
    log.info("host online", { hostId, running: running.length, max });
    this.events.hostOnline(hostId, running);
  }

  /** Called after an admin approves: promote a waiting connection without a reconnect. */
  notifyApproved(hostId: string) {
    const ws = this.pending.get(hostId);
    if (!ws) return;
    const host = this.store.hostById(hostId);
    this.accept(ws, hostId, [], host?.max_sessions ?? 0);
  }

  // ---- control from host ----
  private onControl(c: Conn, msg: HostMsg) {
    switch (msg.type) {
      case "heartbeat":
        c.running = msg.running; c.max = msg.max;
        this.store.touchHost(c.hostId, { max_sessions: msg.max });
        this.events.heartbeat(c.hostId);
        return;
      case "session.started": this.events.sessionStarted(msg.sid); return;
      case "session.ended": this.events.sessionEnded(msg.sid, msg.reason, msg.detail); return;
      case "pty.replay": c.streams.get(msg.stream)?.onReplay?.(); return;
      case "pty.closed": this.endStream(c, msg.stream); return;
      case "port.open": c.streams.get(msg.stream)?.onOpen?.(); return;
      case "port.error": c.streams.get(msg.stream)?.onError?.(msg.msg); this.endStream(c, msg.stream); return;
      case "port.close": this.endStream(c, msg.stream); return;
      case "hello": return;
    }
  }

  private endStream(c: Conn, stream: number) {
    const s = c.streams.get(stream);
    if (!s) return;
    c.streams.delete(stream);
    s.onClose();
  }

  // ---- commands to host ----
  createSession(hostId: string, spec: SessionSpec): boolean {
    const c = this.conns.get(hostId);
    if (!c) return false;
    this.send(c, { type: "session.create", spec });
    c.running += 1;
    return true;
  }

  destroySession(hostId: string, sid: string) {
    const c = this.conns.get(hostId);
    if (c) this.send(c, { type: "session.destroy", sid });
  }

  openPty(hostId: string, sid: string, size: Size, sub: StreamSub): PtyHandle | null {
    const c = this.conns.get(hostId);
    if (!c) return null;
    const stream = c.nextStream; c.nextStream += 2;
    c.streams.set(stream, sub);
    this.send(c, { type: "pty.open", sid, stream, size });
    return {
      write: (d) => c.ws.send(encodeFrame(stream, d)),
      resize: (sz) => this.send(c, { type: "pty.resize", sid, size: sz }),
      close: () => { if (c.streams.delete(stream)) this.send(c, { type: "pty.close", stream }); },
    };
  }

  dialPort(hostId: string, sid: string, port: number, sub: Omit<StreamSub, "onOpen" | "onError" | "onReplay">): Promise<PortHandle> {
    const c = this.conns.get(hostId);
    if (!c) return Promise.reject(new Error("host offline"));
    const stream = c.nextStream; c.nextStream += 2;
    const { promise, resolve, reject } = Promise.withResolvers<PortHandle>();
    const handle: PortHandle = {
      write: (d) => c.ws.send(encodeFrame(stream, d)),
      close: () => { if (c.streams.delete(stream)) this.send(c, { type: "port.close", stream }); },
    };
    c.streams.set(stream, {
      onData: sub.onData,
      onOpen: () => resolve(handle),
      onError: (m) => reject(new Error(m)),
      onClose: sub.onClose,
    });
    this.send(c, { type: "port.dial", sid, port, stream });
    return promise;
  }
}
