// Docker compose → sidecar services. The CP accepts a compose document as-is
// (the catalog in presets/services.yaml, or the `compose` field on POST /sessions,
// which a parent app can fill with a repo's own docker-compose.yml) and keeps the
// subset the daemon can run: image, environment, command, start order and a TCP
// readiness port. Keys that only describe a developer's machine are ignored;
// keys that would change what runs on the host are refused (decision 21).
// Pure: no filesystem, no docker.
import type { ServiceDecl } from './store'
import { badRequest } from './errors'
import { validateEnv } from './env'
import { noServices, validateName, validateReady, type ValidatedServices } from './services'

/** Keys the translation acts on. */
const HANDLED = new Set([
  'image',
  'build',
  'environment',
  'command',
  'depends_on',
  'expose',
  'ports',
  'x-sandboxd',
])
/** Keys that describe a laptop setup (host ports, bind mounts, restart policy…) and are safely dropped. */
const IGNORED = new Set([
  'volumes',
  'restart',
  'container_name',
  'networks',
  'profiles',
  'deploy',
  'logging',
  'labels',
  'healthcheck',
  'stdin_open',
  'tty',
  'pull_policy',
  'platform',
  'env_file',
  'hostname',
  'working_dir',
  'init',
  'develop',
  'attach',
  'stop_grace_period',
  'stop_signal',
  'annotations',
  'scale',
  'links',
  'external_links',
])
/** Keys that would change what runs on the host. Refused so the caller fixes the file rather than getting a surprise. */
const REJECTED = new Set([
  'entrypoint',
  'privileged',
  'cap_add',
  'cap_drop',
  'devices',
  'network_mode',
  'pid',
  'ipc',
  'uts',
  'user',
  'userns_mode',
  'extra_hosts',
  'sysctls',
  'security_opt',
  'cgroup',
  'cgroup_parent',
  'volumes_from',
  'extends',
  'tmpfs',
  'read_only',
  'runtime',
  'isolation',
  'group_add',
  'dns',
  'dns_search',
  'dns_opt',
  'mac_address',
  'domainname',
  'shm_size',
  'ulimits',
  'oom_kill_disable',
  'oom_score_adj',
  'storage_opt',
  'device_cgroup_rules',
  'credential_spec',
  'configs',
  'secrets',
  'gpus',
  'models',
  'provider',
  'post_start',
  'pre_stop',
  'label_file',
  'blkio_config',
  'mem_limit',
  'mem_reservation',
  'mem_swappiness',
  'memswap_limit',
  'cpus',
  'cpu_count',
  'cpu_percent',
  'cpu_shares',
  'cpu_period',
  'cpu_quota',
  'cpu_rt_runtime',
  'cpu_rt_period',
  'cpuset',
  'pids_limit',
])
const X_KEYS = new Set(['ready', 'sandbox_env', 'secret_env'])

/** Translate a compose document (YAML text or a parsed object) into sidecar services, in start order. */
export function parseCompose(input: unknown, at = 'compose'): ValidatedServices {
  if (input === undefined || input === null) return noServices()
  let doc: unknown = input
  if (typeof input === 'string') {
    if (!input.trim()) return noServices()
    try {
      doc = Bun.YAML.parse(input)
    } catch (e) {
      throw badRequest(`${at}: invalid YAML: ${(e as Error).message}`)
    }
  }
  if (!isObj(doc)) throw badRequest(`${at} must be a compose document (YAML text or object)`)
  const services = doc.services
  if (services === undefined || services === null) return noServices()
  if (!isObj(services)) throw badRequest(`${at}.services must be a map`)

  const out = noServices()
  const deps = new Map<string, string[]>()
  for (const [key, raw] of Object.entries(services)) {
    const sat = `${at}.services.${key}`
    if (!isObj(raw)) throw badRequest(`${sat} must be a map`)
    if (raw.build !== undefined && raw.image === undefined) continue // the repo's own app: that is what the sandbox runs
    const name = validateName(sat, key)
    for (const k of Object.keys(raw)) {
      if (HANDLED.has(k) || IGNORED.has(k) || k.startsWith('x-')) continue
      throw badRequest(
        `${sat}.${k} is not supported${REJECTED.has(k) ? '' : ' (unknown key)'}`,
      )
    }
    if (typeof raw.image !== 'string' || !raw.image.trim())
      throw badRequest(`${sat}.image is required`)
    const x = raw['x-sandboxd'] === undefined ? {} : raw['x-sandboxd']
    if (!isObj(x)) throw badRequest(`${sat}.x-sandboxd must be a map`)
    for (const k of Object.keys(x))
      if (!X_KEYS.has(k)) throw badRequest(`${sat}.x-sandboxd.${k} is not supported`)

    const decl: ServiceDecl = {
      name,
      image: interpolate(raw.image.trim(), `${sat}.image`),
      env: validateEnv(
        `${sat}.environment`,
        environment(raw.environment, `${sat}.environment`),
      ),
      cmd: command(raw.command, `${sat}.command`),
      ready:
        x.ready !== undefined
          ? validateReady(`${sat}.x-sandboxd`, x.ready)
          : readyFromPorts(raw, sat),
    }
    if (out.decls.some((d) => d.name === name)) throw badRequest(`${sat}: duplicated`)
    out.decls.push(decl)
    const secret = validateEnv(`${sat}.x-sandboxd.secret_env`, x.secret_env)
    if (Object.keys(secret).length) out.secrets[name] = secret
    const sandbox = validateEnv(`${sat}.x-sandboxd.sandbox_env`, x.sandbox_env)
    if (Object.keys(sandbox).length) out.sandbox_env[name] = sandbox
    deps.set(name, dependsOn(raw.depends_on, `${sat}.depends_on`))
  }
  out.decls = startOrder(out.decls, deps, at)
  return out
}

const isObj = (v: unknown): v is Record<string, unknown> =>
  !!v && typeof v === 'object' && !Array.isArray(v)

/** `environment` as a map (scalars stringified, null dropped) or a `KEY=value` list. */
function environment(raw: unknown, at: string): Record<string, string> {
  const out: Record<string, string> = {}
  if (raw === undefined || raw === null) return out
  if (Array.isArray(raw)) {
    for (const item of raw) {
      if (typeof item !== 'string') throw badRequest(`${at} entries must be KEY=value strings`)
      const i = item.indexOf('=')
      if (i < 0) {
        out[item] = ''
        continue
      } // bare KEY takes the value from the host shell in compose; there is none here
      out[item.slice(0, i)] = interpolate(item.slice(i + 1), at)
    }
    return out
  }
  if (!isObj(raw)) throw badRequest(`${at} must be a map or a list`)
  for (const [k, v] of Object.entries(raw)) {
    if (v === null || v === undefined) continue
    if (typeof v === 'object') throw badRequest(`${at}.${k} must be a scalar`)
    out[k] = interpolate(String(v), `${at}.${k}`)
  }
  return out
}

function command(raw: unknown, at: string): string[] | null {
  if (raw === undefined || raw === null) return null
  if (typeof raw === 'string') {
    const words = splitWords(interpolate(raw, at))
    return words.length ? words : null
  }
  if (
    Array.isArray(raw) &&
    raw.length &&
    raw.every((c) => typeof c === 'string' || typeof c === 'number')
  )
    return raw.map((c) => interpolate(String(c), at))
  throw badRequest(`${at} must be a string or a list of strings`)
}

/** Readiness port when `x-sandboxd.ready` is absent: first `expose` entry, else the container side of the first `ports` entry. */
function readyFromPorts(raw: Record<string, unknown>, at: string): ServiceDecl['ready'] {
  const expose = Array.isArray(raw.expose) ? raw.expose[0] : undefined
  if (expose !== undefined) {
    const p = containerPort(String(expose))
    if (p) return { port: p, timeout_s: 60 }
    throw badRequest(`${at}.expose: cannot read a port from "${expose}"`)
  }
  const port = Array.isArray(raw.ports) ? raw.ports[0] : undefined
  if (port === undefined) return null
  const p = isObj(port)
    ? containerPort(String(port.target ?? ''), String(port.protocol ?? 'tcp'))
    : containerPort(String(port))
  return p ? { port: p, timeout_s: 60 } : null
}

/** "5432", "5433:5432", "127.0.0.1:5433:5432", "5432/tcp" → 5432. UDP and ranges → undefined. */
function containerPort(s: string, protocol?: string): number | undefined {
  const [spec, proto = protocol ?? 'tcp'] = s.split('/')
  if (proto !== 'tcp') return undefined
  const last = spec!.split(':').pop() ?? ''
  if (!/^\d+$/.test(last)) return undefined
  const n = Number(last)
  return n >= 1 && n <= 65535 ? n : undefined
}

function dependsOn(raw: unknown, at: string): string[] {
  if (raw === undefined || raw === null) return []
  if (Array.isArray(raw)) {
    if (!raw.every((d) => typeof d === 'string'))
      throw badRequest(`${at} entries must be service names`)
    return raw as string[]
  }
  if (isObj(raw)) return Object.keys(raw)
  throw badRequest(`${at} must be a list or a map`)
}

/** Dependencies first, otherwise file order. Dependencies on skipped (build-only) services are dropped. */
function startOrder(
  decls: ServiceDecl[],
  deps: Map<string, string[]>,
  at: string,
): ServiceDecl[] {
  const byName = new Map(decls.map((d) => [d.name, d]))
  const done = new Set<string>()
  const stack: string[] = []
  const out: ServiceDecl[] = []
  const visit = (name: string) => {
    if (done.has(name)) return
    const i = stack.indexOf(name)
    if (i >= 0)
      throw badRequest(`${at}: depends_on cycle: ${[...stack.slice(i), name].join(' -> ')}`)
    stack.push(name)
    for (const d of deps.get(name) ?? []) if (byName.has(d)) visit(d)
    stack.pop()
    done.add(name)
    out.push(byName.get(name)!)
  }
  for (const d of decls) visit(d.name)
  return out
}

/** Compose variable interpolation against an empty environment: `${X:-d}` → d, `${X}` → "", `$$` → `$`, `${X:?msg}` → 400. */
export function interpolate(s: string, at: string): string {
  if (!s.includes('$')) return s
  return s.replace(
    /\$(?:(\$)|\{([^}]*)\}|([A-Za-z_][A-Za-z0-9_]*))/g,
    (m, dollar, braced, bare) => {
      if (dollar) return '$'
      if (bare !== undefined) return ''
      const b = braced as string
      const op = /^([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-+?])([\s\S]*))?$/.exec(b)
      if (!op) throw badRequest(`${at}: bad substitution "${m}"`)
      const [, name, kind, arg = ''] = op
      switch (kind) {
        case undefined:
          return ''
        case '-':
        case ':-':
          return arg
        case '+':
        case ':+':
          return ''
        default:
          throw badRequest(
            `${at}: ${arg || `variable ${name} is required`} (set it with \${${name}:-default} — the control plane has no environment to read it from)`,
          )
      }
    },
  )
}

/** Shell-style word splitting for `command: "redis-server --appendonly yes"`. */
export function splitWords(s: string): string[] {
  const out: string[] = []
  let cur = '',
    quote: string | null = null,
    has = false
  for (let i = 0; i < s.length; i++) {
    const c = s[i]!
    if (quote) {
      if (c === quote) quote = null
      else if (c === '\\' && quote === '"' && i + 1 < s.length) cur += s[++i]
      else cur += c
    } else if (c === '"' || c === "'") {
      quote = c
      has = true
    } else if (c === '\\' && i + 1 < s.length) cur += s[++i]
    else if (/\s/.test(c)) {
      if (cur || has) out.push(cur)
      cur = ''
      has = false
    } else cur += c
  }
  if (cur || has) out.push(cur)
  return out
}
