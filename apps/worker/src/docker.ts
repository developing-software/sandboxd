// SandboxDriver backed by the Docker Engine API over a unix socket.
// PTY attach = `exec` with Tty:true, hijacked on a raw socket (fetch can't do upgrades).
import type { Socket } from 'bun'
import type { Msg } from '@sandboxd/core/messages'
import type { CreateOpts, Duplex, Managed, PtyStream, SandboxDriver } from './driver'
import { Bytes } from '@sandboxd/core/bytes'

interface Created {
  Id: string
}
interface ContainerInspect {
  NetworkSettings?: { IPAddress?: string; Networks?: Record<string, { IPAddress?: string }> }
}
interface ContainerSummary {
  Id: string
  Labels?: Record<string, string>
}
interface NetworkSummary {
  Id: string
  Name: string
  Labels?: Record<string, string>
}

/** Every container and network carries these; anything created without them is invisible
 *  to the orphan sweep on start. */
const LABEL = {
  managed: 'sandboxd.managed',
  sid: 'sandboxd.sid',
  host: 'sandboxd.host',
  role: 'sandboxd.role',
} as const

export class DockerDriver implements SandboxDriver {
  /** `owner` scopes create/list/cleanup to this agent identity, so several agents
   *  sharing one Docker host never touch each other's sandboxes. */
  constructor(
    private sock = '/var/run/docker.sock',
    private owner = 'unknown',
  ) {}

  private async api<T = unknown>(
    method: string,
    path: string,
    body?: unknown,
  ): Promise<T | null> {
    const r = await fetch(`http://docker${path}`, {
      unix: this.sock,
      method,
      headers: body ? { 'content-type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    })
    if (r.status === 204) return null
    const text = await r.text()
    if (!r.ok) throw new Error(`docker ${method} ${path} -> ${r.status}: ${text.trim()}`)
    return text ? (JSON.parse(text) as T) : null
  }

  private labels(sid: string, role?: string): Record<string, string> {
    return {
      [LABEL.managed]: '1',
      [LABEL.sid]: sid,
      [LABEL.host]: this.owner,
      ...(role ? { [LABEL.role]: role } : {}),
    }
  }

  private filters(extra: string[] = []) {
    return encodeURIComponent(
      JSON.stringify({
        label: [`${LABEL.managed}=1`, `${LABEL.host}=${this.owner}`, ...extra],
      }),
    )
  }

  private async pull(image: string) {
    const r = await fetch(
      `http://docker/images/create?fromImage=${encodeURIComponent(image)}`,
      { unix: this.sock, method: 'POST' },
    )
    if (!r.ok) throw new Error(`docker pull ${image} -> ${r.status}: ${await r.text()}`)
    await r.text() // drain progress stream until complete
  }

  async create({ sid, image, role, network, alias, env, cmd }: CreateOpts): Promise<string> {
    const name = role === 'sandbox' ? `sandboxd-${sid}` : `sandboxd-${sid}-${alias ?? 'svc'}`
    const body = {
      Image: image,
      // The sandbox is kept idle so a PTY can be exec'd into it; a service runs whatever the image runs unless overridden.
      ...(role === 'sandbox' ? { Cmd: ['sleep', 'infinity'] } : cmd ? { Cmd: cmd } : {}),
      Env: env ? Object.entries(env).map(([k, v]) => `${k}=${v}`) : undefined,
      Labels: this.labels(sid, role),
      HostConfig: {
        Init: true,
        // host.docker.internal lets a sandbox reach services on the host (e.g. a LiteLLM proxy on localhost).
        ExtraHosts: ['host.docker.internal:host-gateway'],
        ...(network ? { NetworkMode: network } : {}),
      },
      ...(network && alias
        ? { NetworkingConfig: { EndpointsConfig: { [network]: { Aliases: [alias] } } } }
        : {}),
    }
    const createPath = `/containers/create?name=${name}`
    let res: Created | null
    try {
      res = await this.api<Created>('POST', createPath, body)
    } catch (e) {
      if (!String(e).includes('-> 404')) throw e
      await this.pull(image)
      res = await this.api<Created>('POST', createPath, body)
    }
    if (!res) throw new Error('docker create returned no body')
    await this.api('POST', `/containers/${res.Id}/start`)
    return res.Id
  }

  async attach(
    id: string,
    cmd: string[],
    env: Record<string, string>,
    size: Msg.Size,
  ): Promise<PtyStream> {
    const exec = await this.api<Created>('POST', `/containers/${id}/exec`, {
      AttachStdin: true,
      AttachStdout: true,
      AttachStderr: true,
      Tty: true,
      Cmd: cmd,
      Env: Object.entries(env).map(([k, v]) => `${k}=${v}`),
    })
    if (!exec) throw new Error('docker exec returned no body')
    const execId = exec.Id

    // Bytes (or an exit) can arrive before the caller registers its handlers, e.g. a
    // command that prints immediately. Buffer them instead of dropping them.
    let dataCb: ((d: Uint8Array) => void) | null = null
    let exitCb: (() => void) | null = null
    const early: Uint8Array[] = []
    let exited = false
    const emit = (d: Uint8Array) => {
      if (dataCb) dataCb(d)
      else early.push(d.slice())
    }
    const exit = () => {
      exited = true
      exitCb?.()
    }
    let headerDone = false
    let pending: Uint8Array = new Uint8Array(0)
    const opened = Promise.withResolvers<Socket<undefined>>()

    const socket = await Bun.connect({
      unix: this.sock,
      socket: {
        open(s) {
          const body = JSON.stringify({ Detach: false, Tty: true })
          s.write(
            `POST /exec/${execId}/start HTTP/1.1\r\nHost: docker\r\nContent-Type: application/json\r\n` +
              `Connection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: ${body.length}\r\n\r\n${body}`,
          )
        },
        data(s, incoming) {
          let chunk: Uint8Array = incoming
          if (!headerDone) {
            pending = Bytes.concat([pending, chunk])
            const idx = Bytes.crlf2(pending)
            if (idx < 0) return
            const head = new TextDecoder().decode(pending.subarray(0, idx))
            if (!/^HTTP\/1\.1 (101|200)/.test(head)) {
              opened.reject(new Error(`exec start failed: ${head.split('\r\n')[0]}`))
              s.end()
              return
            }
            headerDone = true
            opened.resolve(s)
            chunk = pending.subarray(idx + 4)
            pending = new Uint8Array(0)
            if (chunk.length === 0) return
          }
          emit(chunk)
        },
        close() {
          exit()
        },
        error(_, e) {
          opened.reject(e)
          exit()
        },
      },
    })
    await opened.promise
    const resize = (sz: Msg.Size) =>
      this.api('POST', `/exec/${execId}/resize?h=${sz.rows}&w=${sz.cols}`).catch(() => {})
    await resize(size)

    return {
      write: (d) => {
        socket.write(d)
      },
      resize: async (sz) => {
        await resize(sz)
      },
      close: () => socket.end(),
      onData: (cb) => {
        dataCb = cb
        for (const d of early.splice(0)) cb(d)
      },
      onExit: (cb) => {
        exitCb = cb
        if (exited) cb()
      },
    }
  }

  async dial(id: string, port: number): Promise<Duplex> {
    const info = await this.api<ContainerInspect>('GET', `/containers/${id}/json`)
    const nets = info?.NetworkSettings?.Networks ?? {}
    const ip = Object.values(nets)[0]?.IPAddress || info?.NetworkSettings?.IPAddress
    if (!ip) throw new Error('container has no IP address')

    let dataCb: (d: Uint8Array) => void = () => {}
    let closeCb: () => void = () => {}
    const socket = await Bun.connect({
      hostname: ip,
      port,
      socket: {
        data(_, chunk) {
          dataCb(chunk)
        },
        close() {
          closeCb()
        },
        error() {
          closeCb()
        },
        connectError() {
          closeCb()
        },
      },
    })
    return {
      write: (d) => {
        socket.write(d)
      },
      end: () => socket.end(),
      onData: (cb) => {
        dataCb = cb
      },
      onClose: (cb) => {
        closeCb = cb
      },
    }
  }

  async destroy(id: string): Promise<void> {
    await this.api('DELETE', `/containers/${id}?force=1&v=1`).catch((e) => {
      // 404: already gone. 409: removal already in progress. Both mean "done" for us.
      if (!/-> (404|409)/.test(String(e))) throw e
    })
  }

  async createNetwork(sid: string): Promise<string> {
    const name = `sandboxd-${sid}`
    await this.api('POST', '/networks/create', {
      Name: name,
      Driver: 'bridge',
      Labels: this.labels(sid),
    })
    return name
  }

  async removeNetwork(sid: string): Promise<void> {
    await this.api('DELETE', `/networks/sandboxd-${sid}`).catch((e) => {
      if (!/-> 404/.test(String(e))) throw e
    })
  }

  async listManaged(): Promise<{ containers: Managed[]; networks: Managed[] }> {
    const containers =
      (await this.api<ContainerSummary[]>(
        'GET',
        `/containers/json?all=1&filters=${this.filters()}`,
      )) ?? []
    const networks =
      (await this.api<NetworkSummary[]>('GET', `/networks?filters=${this.filters()}`)) ?? []
    return {
      containers: containers.map((c) => ({ id: c.Id, sid: c.Labels?.[LABEL.sid] ?? '?' })),
      networks: networks.map((n) => ({ id: n.Id, sid: n.Labels?.[LABEL.sid] ?? '?' })),
    }
  }
}
