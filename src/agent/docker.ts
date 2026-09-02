// SandboxDriver backed by the Docker Engine API over a unix socket.
// PTY attach = `exec` with Tty:true, hijacked on a raw socket (fetch can't do upgrades).
import type { Socket } from "bun";
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
  create(opts: CreateOpts): Promise<string>;
  attach(id: string, cmd: string[], env: Record<string, string>, size: Size): Promise<PtyStream>;
  dial(id: string, port: number): Promise<Duplex>;
  destroy(id: string): Promise<void>;
  listManaged(): Promise<{ id: string; sid: string }[]>;
}

export class DockerDriver implements SandboxDriver {
  /** `owner` scopes create/list/cleanup to this agent identity, so several agents
   *  sharing one Docker host never touch each other's sandboxes. */
  constructor(private sock = "/var/run/docker.sock", private owner = "unknown") {}

  private async api(method: string, path: string, body?: unknown): Promise<any> {
    const r = await fetch(`http://docker${path}`, {
      unix: this.sock, method,
      headers: body ? { "content-type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (r.status === 204) return null;
    const text = await r.text();
    if (!r.ok) throw new Error(`docker ${method} ${path} -> ${r.status}: ${text.trim()}`);
    return text ? JSON.parse(text) : null;
  }

  private async pull(image: string) {
    const r = await fetch(`http://docker/images/create?fromImage=${encodeURIComponent(image)}`, { unix: this.sock, method: "POST" });
    if (!r.ok) throw new Error(`docker pull ${image} -> ${r.status}: ${await r.text()}`);
    await r.text(); // drain progress stream until complete
  }

  async create({ sid, image }: CreateOpts): Promise<string> {
    const body = {
      Image: image,
      Cmd: ["sleep", "infinity"],
      Labels: { "devagents.managed": "1", "devagents.sid": sid, "devagents.host": this.owner },
      // host.docker.internal lets a sandbox reach services on the host (e.g. a LiteLLM proxy on localhost).
      HostConfig: { Init: true, ExtraHosts: ["host.docker.internal:host-gateway"] },
    };
    let res: any;
    try {
      res = await this.api("POST", `/containers/create?name=devagents-${sid}`, body);
    } catch (e) {
      if (!String(e).includes("-> 404")) throw e;
      await this.pull(image);
      res = await this.api("POST", `/containers/create?name=devagents-${sid}`, body);
    }
    await this.api("POST", `/containers/${res.Id}/start`);
    return res.Id as string;
  }

  async attach(id: string, cmd: string[], env: Record<string, string>, size: Size): Promise<PtyStream> {
    const exec = await this.api("POST", `/containers/${id}/exec`, {
      AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true,
      Cmd: cmd, Env: Object.entries(env).map(([k, v]) => `${k}=${v}`),
    });
    const execId = exec.Id as string;

    let dataCb: (d: Uint8Array) => void = () => {};
    let exitCb: () => void = () => {};
    let headerDone = false;
    let pending = new Uint8Array(0);
    const opened = Promise.withResolvers<Socket<undefined>>();

    const socket = await Bun.connect({
      unix: this.sock,
      socket: {
        open(s) {
          const body = JSON.stringify({ Detach: false, Tty: true });
          s.write(
            `POST /exec/${execId}/start HTTP/1.1\r\nHost: docker\r\nContent-Type: application/json\r\n` +
            `Connection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: ${body.length}\r\n\r\n${body}`,
          );
        },
        data(s, incoming) {
          let chunk: Uint8Array = incoming;
          if (!headerDone) {
            const merged = new Uint8Array(pending.length + chunk.length);
            merged.set(pending); merged.set(chunk, pending.length); pending = merged;
            const idx = indexOfCRLF2(pending);
            if (idx < 0) return;
            const head = new TextDecoder().decode(pending.subarray(0, idx));
            if (!/^HTTP\/1\.1 (101|200)/.test(head)) {
              opened.reject(new Error(`exec start failed: ${head.split("\r\n")[0]}`));
              s.end();
              return;
            }
            headerDone = true;
            opened.resolve(s);
            chunk = pending.subarray(idx + 4);
            pending = new Uint8Array(0);
            if (chunk.length === 0) return;
          }
          dataCb(chunk);
        },
        close() { exitCb(); },
        error(_, e) { opened.reject(e); exitCb(); },
      },
    });
    await opened.promise;
    await this.api("POST", `/exec/${execId}/resize?h=${size.rows}&w=${size.cols}`).catch(() => {});

    return {
      write: (d) => { socket.write(d); },
      resize: async (sz) => { await this.api("POST", `/exec/${execId}/resize?h=${sz.rows}&w=${sz.cols}`).catch(() => {}); },
      close: () => socket.end(),
      onData: (cb) => { dataCb = cb; },
      onExit: (cb) => { exitCb = cb; },
    };
  }

  async dial(id: string, port: number): Promise<Duplex> {
    const info = await this.api("GET", `/containers/${id}/json`);
    const nets = info?.NetworkSettings?.Networks ?? {};
    const ip: string | undefined = Object.values<any>(nets)[0]?.IPAddress || info?.NetworkSettings?.IPAddress;
    if (!ip) throw new Error("container has no IP address");

    let dataCb: (d: Uint8Array) => void = () => {};
    let closeCb: () => void = () => {};
    const socket = await Bun.connect({
      hostname: ip, port,
      socket: {
        data(_, chunk) { dataCb(chunk); },
        close() { closeCb(); },
        error() { closeCb(); },
        connectError(_, e) { closeCb(); },
      },
    });
    return {
      write: (d) => { socket.write(d); },
      end: () => socket.end(),
      onData: (cb) => { dataCb = cb; },
      onClose: (cb) => { closeCb = cb; },
    };
  }

  async destroy(id: string): Promise<void> {
    await this.api("DELETE", `/containers/${id}?force=1&v=1`).catch((e) => {
      // 404: already gone. 409: removal already in progress. Both mean "done" for us.
      if (!/-> (404|409)/.test(String(e))) throw e;
    });
  }

  async listManaged(): Promise<{ id: string; sid: string }[]> {
    const filters = encodeURIComponent(JSON.stringify({ label: ["devagents.managed=1", `devagents.host=${this.owner}`] }));
    const list: any[] = (await this.api("GET", `/containers/json?all=1&filters=${filters}`)) ?? [];
    return list.map((c) => ({ id: c.Id, sid: c.Labels?.["devagents.sid"] ?? "?" }));
  }
}

function indexOfCRLF2(buf: Uint8Array): number {
  for (let i = 0; i + 3 < buf.length; i++) {
    if (buf[i] === 13 && buf[i + 1] === 10 && buf[i + 2] === 13 && buf[i + 3] === 10) return i;
  }
  return -1;
}
