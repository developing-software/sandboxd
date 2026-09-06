// The public surface of @sandboxd/sdk. Everything under ./generated is written by hey-api
// from api/client.yaml; this file is the hand-written half, and it is the only place that
// decides what a caller sees.
//
// The operator surface is a second document with no published client (DESIGN.md decision
// 27), so nothing here enrols a worker or lists the fleet. A tool that operates a fleet
// generates its own types from api/admin.yaml, which is what lets that document break.
import { type Client, createClient, createConfig } from './generated/client'
import type { ClientOptions } from './generated/types.gen'

export interface SandboxdOptions {
  /** The control plane, e.g. https://sandboxd.example.com. */
  baseUrl: string
  /** SANDBOXD_SERVICE_TOKEN, sent as `Authorization: Bearer`. */
  serviceToken: string
  /** Injected by tests, and by a server that wraps its own transport. */
  fetch?: typeof globalThis.fetch
}

/**
 * A client bound to one control plane. Pass it to any call as `{ client }`; without one
 * the generated functions fall back to a module-level client pointed at localhost, which
 * is a convenience for a script and a trap for a server.
 *
 * The token belongs on a server. Everything here is service-token gated, so a browser
 * that held one could create sandboxes for any owner. Every call also carries the owner
 * it acts for, in the `X-Sandboxd-Owner` header — the token says which app is calling,
 * the header says which of its users for.
 */
export function createSandboxd(o: SandboxdOptions): Client {
  return createClient(
    createConfig<ClientOptions>({
      baseUrl: o.baseUrl,
      auth: () => o.serviceToken,
      ...(o.fetch === undefined ? {} : { fetch: o.fetch }),
    }),
  )
}

// `attachTerminal` is generated but deliberately not exported: GET on the terminal path is
// a WebSocket upgrade, and a fetch against it can only fail. Call `openTerminal` and open
// the `url` it returns from a browser — the credential is already in it.
export {
  createSandbox,
  endSandbox,
  getSandbox,
  healthz,
  listSandboxes,
  openPreview,
  openTerminal,
  type Options,
} from './generated/sdk.gen'

export type { Client, Config } from './generated/client'

export type {
  CreateSandbox,
  ErrorModel,
  Health,
  Issue,
  Link,
  PreviewBody,
  SandboxView,
} from './generated/types.gen'
