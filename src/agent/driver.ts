// The sandbox driver contract (design decision 14). Only `docker.ts` implements
// it in v1; Podman/Firecracker would live beside it and plug in at agent/main.ts.
import type { Size } from "../protocol/messages.ts";

export interface PtyStream {
  write(data: Uint8Array | string): void;
  resize(size: Size): Promise<void>;
  close(): void;
  onData(cb: (data: Uint8Array) => void): void;
  onExit(cb: () => void): void;
}

export interface Duplex {
  write(data: Uint8Array): void;
  end(): void;
  onData(cb: (data: Uint8Array) => void): void;
  onClose(cb: () => void): void;
}

export interface CreateOpts { sid: string; image: string }

export interface SandboxDriver {
  /** Create and start a sandbox from `image`; returns the driver's id for it. */
  create(opts: CreateOpts): Promise<string>;
  /** Exec `cmd` with `env` in a PTY inside the sandbox. */
  attach(id: string, cmd: string[], env: Record<string, string>, size: Size): Promise<PtyStream>;
  /** TCP connection to `port` inside the sandbox (preview proxy). */
  dial(id: string, port: number): Promise<Duplex>;
  destroy(id: string): Promise<void>;
  /** Sandboxes this agent identity created (for orphan cleanup on start). */
  listManaged(): Promise<{ id: string; sid: string }[]>;
}
