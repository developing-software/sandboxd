// The sandbox driver contract (design decision 14). Only `docker.ts` implements
// it in v1; Podman/Firecracker would live beside it and plug in at agent/main.ts.
import type { Size } from '@sandboxd/core/messages'

export interface PtyStream {
  write(data: Uint8Array | string): void
  resize(size: Size): Promise<void>
  close(): void
  onData(cb: (data: Uint8Array) => void): void
  onExit(cb: () => void): void
}

export interface Duplex {
  write(data: Uint8Array): void
  end(): void
  onData(cb: (data: Uint8Array) => void): void
  onClose(cb: () => void): void
}

export interface CreateOpts {
  sid: string
  image: string
  /** `sandbox`: kept idle so a PTY can be exec'd into it. `service`: runs the image's own command. */
  role: 'sandbox' | 'service'
  /** Session network to join (from createNetwork) and the DNS alias on it. Omit for the default network. */
  network?: string
  alias?: string
  /** Env set at container create (services). The sandbox gets its env at exec time instead. */
  env?: Record<string, string>
  /** Command override for a service (compose `command:`). The sandbox is always kept idle. */
  cmd?: string[]
}

export interface Managed {
  id: string
  sid: string
}

export interface SandboxDriver {
  /** Create and start a container; returns the driver's id for it. */
  create(opts: CreateOpts): Promise<string>
  /** Exec `cmd` with `env` in a PTY inside the sandbox. */
  attach(
    id: string,
    cmd: string[],
    env: Record<string, string>,
    size: Size,
  ): Promise<PtyStream>
  /** TCP connection to `port` inside a container (preview proxy, readiness probes). Rejects when refused. */
  dial(id: string, port: number): Promise<Duplex>
  destroy(id: string): Promise<void>
  /** Private network for one session's containers. Returns its name. */
  createNetwork(sid: string): Promise<string>
  /** Idempotent. */
  removeNetwork(sid: string): Promise<void>
  /** Containers and networks this agent identity created (for orphan cleanup on start). */
  listManaged(): Promise<{ containers: Managed[]; networks: Managed[] }>
}
