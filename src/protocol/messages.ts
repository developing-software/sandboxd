// Control messages exchanged as JSON text frames on the host <-> CP tunnel.

export type EndReason = "closed" | "idle" | "failed" | "lost" | "exited";

export interface SessionSpec {
  sid: string;
  repo: string;
  branch: string;
  base_branch: string | null;
  prompt: string;
  image: string;
  idle_timeout_s: number;
  /** Injected into the PTY process only. Never persisted by either side. */
  env: Record<string, string>;
}

export interface Size { cols: number; rows: number }

// host -> cp
export type HostMsg =
  | { type: "hello"; name: string; fingerprint: string; running: string[]; max_sessions: number }
  | { type: "heartbeat"; running: number; max: number }
  | { type: "session.started"; sid: string }
  | { type: "session.ended"; sid: string; reason: EndReason; detail?: string }
  | { type: "pty.replay"; stream: number } // binary replay frames follow, then live
  | { type: "pty.closed"; stream: number }
  | { type: "port.open"; stream: number }
  | { type: "port.error"; stream: number; msg: string }
  | { type: "port.close"; stream: number };

// cp -> host
export type CpMsg =
  | { type: "hello.ok"; host_id: string }
  | { type: "hello.pending"; host_id: string; code: string }
  | { type: "hello.rejected"; reason: string }
  | { type: "session.create"; spec: SessionSpec }
  | { type: "session.destroy"; sid: string }
  | { type: "pty.open"; sid: string; stream: number; size: Size }
  | { type: "pty.resize"; sid: string; size: Size }
  | { type: "pty.close"; stream: number }
  | { type: "port.dial"; sid: string; port: number; stream: number }
  | { type: "port.close"; stream: number };

// browser -> cp on the attach socket (binary frames are raw PTY bytes)
export type AttachClientMsg = { type: "resize"; cols: number; rows: number };
// cp -> browser
export type AttachServerMsg = { type: "closed"; reason: string };

export function parseMsg<T>(raw: string): T {
  const m = JSON.parse(raw);
  if (!m || typeof m.type !== "string") throw new Error("bad message");
  return m as T;
}
