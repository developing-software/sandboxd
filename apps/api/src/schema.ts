// Every request and response shape the API speaks, as Zod. The validator reads them for
// 400s, the OpenAPI document reads them for docs, and the UI reads the route types
// through hono/client — one declaration, three consumers.
import { z } from 'zod'
import { Env } from './env'
import { Service } from './services'

export const MIN_IDLE_S = 60
export const DEFAULT_IDLE_S = 1800

/** A map of env vars. Names, reserved keys and the size cap are `env.ts`'s rules. */
const envMap = (name: string) =>
  z.record(z.string(), z.string()).superRefine((v, ctx) => {
    try {
      Env.validate(name, v)
    } catch (e) {
      ctx.addIssue({ code: 'custom', message: (e as Error).message })
    }
  })

const Port = z.int().min(1).max(65535)

export const Ready = z
  .object({
    port: Port.meta({
      description: 'TCP port the worker waits on before starting the sandbox.',
    }),
    timeout_s: z.int().min(1).max(Service.MAX_TIMEOUT_S).default(Service.DEFAULT_TIMEOUT_S),
  })
  .meta({ description: 'Readiness gate for a sidecar.' })

/** A sidecar as the caller declares it. Catalog names are a UI concern; here every service is spelled out. */
export const ServiceInput = z
  .object({
    name: z
      .string()
      .regex(Service.NAME_RE, `must match ${Service.NAME_RE}`)
      .refine((n) => n !== Service.ALIAS, `"${Service.ALIAS}" is reserved`)
      .meta({ description: 'Hostname on the session network.', example: 'db' }),
    image: z.string().trim().min(1).meta({ example: 'postgres:16' }),
    env: envMap('env').default({}),
    secret_env: envMap('secret_env')
      .default({})
      .meta({ description: 'Set at container create, never persisted by the control plane.' }),
    cmd: z.array(z.string()).min(1).nullable().default(null),
    ready: Ready.nullable().default(null),
  })
  .strict()
export type ServiceInput = z.infer<typeof ServiceInput>

export const ServiceDecl = z
  .object({
    name: z.string(),
    image: z.string(),
    env: z.record(z.string(), z.string()),
    cmd: z.array(z.string()).nullable(),
    ready: z.object({ port: z.int(), timeout_s: z.int() }).nullable(),
  })
  .meta({ description: 'A sidecar, secrets stripped.' })

export const CreateSession = z
  .object({
    owner_id: z.string().min(1).meta({
      description: 'The parent app’s user. Every later call must present the same value.',
      example: 'user_42',
    }),
    image: z.string().trim().min(1).meta({ example: 'sandboxd-coding-agent:latest' }),
    cmd: z
      .array(z.string())
      .min(1)
      .optional()
      .meta({ description: 'Exec’d in the PTY. Omit for the image’s default entry.' }),
    idle_timeout_s: z
      .int()
      .min(MIN_IDLE_S)
      .optional()
      .meta({
        description: `Ended after this long without PTY traffic. Default ${DEFAULT_IDLE_S}.`,
      }),
    env: envMap('env').optional().meta({ description: 'Persisted, visible in the API.' }),
    secret_env: envMap('secret_env')
      .optional()
      .meta({ description: 'Forwarded to the worker, never persisted or shown.' }),
    services: z.array(ServiceInput).optional(),
    compose: z
      .union([z.string(), z.record(z.string(), z.unknown())])
      .optional()
      .meta({
        description:
          'A docker compose document (YAML text or object). Its services become sidecars; see DESIGN.md decision 21.',
      }),
  })
  .strict()
  .meta({ description: 'What to run. Presets are resolved before this, by the UI.' })
export type CreateSession = z.infer<typeof CreateSession>
/** What a caller sends: defaults not yet filled in. `hono/client` types `json` with this. */
export type CreateSessionInput = z.input<typeof CreateSession>

export const SessionStatus = z.enum(['queued', 'creating', 'running', 'ended'])
export const EndReason = z.enum(['closed', 'idle', 'failed', 'lost', 'exited'])

export const SessionView = z
  .object({
    id: z.string().meta({ example: 's_ab12cd34' }),
    owner_id: z.string(),
    status: SessionStatus,
    host_id: z.string().nullable(),
    host_online: z.boolean().nullable(),
    queue_position: z.int().nullable(),
    ended_reason: EndReason.nullable(),
    ended_detail: z.string().nullable(),
    image: z.string(),
    cmd: z.array(z.string()).nullable(),
    env: z.record(z.string(), z.string()),
    services: z.array(ServiceDecl),
    idle_timeout_s: z.int(),
    created_at: z.int(),
    started_at: z.int().nullable(),
    ended_at: z.int().nullable(),
  })
  .meta({ description: 'A session. Secrets never appear here.' })
export type SessionView = z.infer<typeof SessionView>

export const HostView = z
  .object({
    id: z.string(),
    name: z.string(),
    status: z.enum(['pending', 'approved', 'revoked']),
    approve_code: z.string().optional().meta({ description: 'Only while pending.' }),
    online: z.boolean(),
    capacity: z.object({ running: z.int(), max: z.int() }).nullable(),
    last_seen_at: z.int().nullable(),
    created_at: z.int(),
  })
  .meta({ description: 'A worker host.' })

export const Owner = z.object({ owner_id: z.string().min(1) })
export const ApproveHost = z.object({ code: z.string().min(1) })
export const PreviewRequest = Owner.extend({ port: Port })
export const AttachQuery = z.object({
  token: z.string().min(1),
  cols: z.coerce.number().int().positive().default(120),
  rows: z.coerce.number().int().positive().default(40),
})

export const AttachToken = z
  .object({ token: z.string(), wss_url: z.string(), expires_in_s: z.int() })
  .meta({ description: 'Single-session, short-lived. Open wss_url from the browser.' })
export const PreviewToken = z
  .object({ token: z.string(), url: z.string(), expires_in_s: z.int() })
  .meta({
    description: 'Open url from the browser; it sets the preview cookie and redirects.',
  })
export const Ok = z.object({ ok: z.literal(true) }).meta({ description: 'Done.' })
export const ErrorBody = z
  .object({
    error: z.string().meta({ description: 'The first problem, in prose.' }),
    issues: z
      .array(z.object({ path: z.string().optional(), message: z.string() }))
      .optional()
      .meta({ description: 'Every failing field, for validation errors.' }),
  })
  .meta({ description: 'Something went wrong.' })
