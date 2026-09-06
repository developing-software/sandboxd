// The public surface of @sandboxd/sdk. Everything under ./generated is written by
// hey-api from openapi.json; this file is the hand-written half, and it is the only place
// that decides what a caller sees.
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
 * that held one could create sandboxes for any owner.
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

// `attach` is generated but deliberately not exported: GET /attach is a WebSocket
// upgrade, and a fetch against it can only fail. Mint a token with mintAttachToken and
// open the `wss_url` it returns.
export {
  approveHost,
  createSandbox,
  endSandbox,
  getSandbox,
  healthz,
  listHosts,
  listSandboxes,
  mintAttachToken,
  mintPreviewToken,
  type Options,
  revokeHost,
} from './generated/sdk.gen'

export type { Client, Config } from './generated/client'

export type {
  ApproveBody,
  AttachToken,
  Capacity,
  CreateSandbox,
  ErrorModel,
  HostView,
  Issue,
  Ok,
  OwnerBody,
  PreviewBody,
  PreviewToken,
  SandboxView,
} from './generated/types.gen'
