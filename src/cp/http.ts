// Parent-app-facing HTTP API. Auth = service token; every session call
// carries owner_id and the CP enforces ownership. No users live here.
type Upgrader = { upgrade(req: Request, opts: { data: AttachData }): boolean };
import type { CpConfig } from "./config.ts";
import type { Store } from "./store.ts";
import type { HostHub } from "./tunnel.ts";
import type { Scheduler } from "./scheduler.ts";
import type { Tokens } from "./tokens.ts";
import { HttpError, badRequest, conflict, notFound, unauthorized } from "../shared/errors.ts";
import { newSessionId } from "../shared/ids.ts";
import type { AttachData } from "./attach.ts";
import { logger } from "../shared/log.ts";

const log = logger("cp.api");

const ATTACH_TTL_MS = 60_000;
const PREVIEW_TTL_MS = 10 * 60_000;

export interface CreateSessionBody {
  owner_id: string; repo: string; prompt: string;
  base_branch?: string; branch?: string; image?: string; idle_timeout_s?: number;
  secrets?: { git_token?: string; anthropic_api_key?: string };
}

export class Api {
  constructor(
    private cfg: CpConfig, private store: Store, private hub: HostHub,
    private sched: Scheduler, private tokens: Tokens,
  ) {}

  async handle(req: Request, server: Upgrader): Promise<Response> {
    try {
      return await this.route(req, server);
    } catch (e) {
      if (e instanceof HttpError) return json({ error: e.message }, e.status);
      console.error(e);
      return json({ error: "internal error" }, 500);
    }
  }

  private async route(req: Request, server: Upgrader): Promise<Response> {
    const url = new URL(req.url);
    const path = url.pathname.replace(/\/+$/, "") || "/";
    const m = req.method;

    if (path === "/healthz") return json({ ok: true });

    if (path === "/attach") {
      const p = this.tokens.verify(url.searchParams.get("token"), "attach");
      if (!p) return json({ error: "invalid or expired attach token" }, 401);
      const data: AttachData = {
        kind: "attach", sid: p.sid, pty: null,
        cols: Number(url.searchParams.get("cols") ?? 120) || 120,
        rows: Number(url.searchParams.get("rows") ?? 40) || 40,
      };
      return server.upgrade(req, { data }) ? new Response(null, { status: 101 }) : json({ error: "websocket upgrade required" }, 426);
    }

    if (path === "/dev" && this.cfg.dev && m === "GET") return this.dev();

    // Everything below needs the service token.
    if (req.headers.get("authorization") !== `Bearer ${this.cfg.serviceToken}`) throw unauthorized();

    if (path === "/hosts" && m === "GET") {
      return json(this.store.listHosts().map((h) => ({
        id: h.id, name: h.name, status: h.status, approve_code: h.status === "pending" ? h.approve_code : undefined,
        online: this.hub.isOnline(h.id), capacity: this.hub.capacity(h.id), last_seen_at: h.last_seen_at, created_at: h.created_at,
      })));
    }
    let mm: RegExpExecArray | null;
    if ((mm = /^\/hosts\/([^/]+)\/approve$/.exec(path)) && m === "POST") {
      const host = this.store.hostById(mm[1]!) ?? (() => { throw notFound("host"); })();
      const body = await readJson<{ code?: string }>(req);
      if (host.status !== "pending") throw conflict(`host is ${host.status}`);
      if (!body.code || body.code.toUpperCase() !== host.approve_code) throw badRequest("approval code does not match");
      this.store.approveHost(host.id);
      log.info("host approved", { hostId: host.id, name: host.name });
      this.hub.notifyApproved(host.id);
      return json({ ok: true });
    }
    if ((mm = /^\/hosts\/([^/]+)\/revoke$/.exec(path)) && m === "POST") {
      if (!this.store.hostById(mm[1]!)) throw notFound("host");
      this.store.revokeHost(mm[1]!);
      return json({ ok: true });
    }

    if (path === "/sessions" && m === "POST") return this.createSession(await readJson<CreateSessionBody>(req));
    if (path === "/sessions" && m === "GET") {
      return json(this.store.listSessions(url.searchParams.get("owner_id") ?? undefined).map((s) => this.view(s)));
    }
    if ((mm = /^\/sessions\/([^/]+)$/.exec(path))) {
      const s = this.owned(mm[1]!, url.searchParams.get("owner_id"));
      if (m === "GET") return json(this.view(s));
      if (m === "DELETE") { this.sched.cancel(s); return json(this.view(this.store.session(s.id)!)); }
    }
    if ((mm = /^\/sessions\/([^/]+)\/attach-token$/.exec(path)) && m === "POST") {
      const body = await readJson<{ owner_id?: string }>(req);
      const s = this.owned(mm[1]!, body.owner_id ?? url.searchParams.get("owner_id"));
      if (s.status !== "running") throw conflict(`session is ${s.status}`);
      const token = this.tokens.sign({ k: "attach", sid: s.id }, ATTACH_TTL_MS);
      const ws = this.cfg.publicUrl.replace(/^http/, "ws");
      return json({ token, wss_url: `${ws}/attach?token=${token}`, expires_in_s: ATTACH_TTL_MS / 1000 });
    }
    if ((mm = /^\/sessions\/([^/]+)\/preview-token$/.exec(path)) && m === "POST") {
      const body = await readJson<{ owner_id?: string; port?: number }>(req);
      const s = this.owned(mm[1]!, body.owner_id ?? url.searchParams.get("owner_id"));
      const port = Number(body.port);
      if (!Number.isInteger(port) || port < 1 || port > 65535) throw badRequest("port must be 1-65535");
      const token = this.tokens.sign({ k: "preview", sid: s.id, port }, PREVIEW_TTL_MS);
      const pub = new URL(this.cfg.publicUrl);
      const portSuffix = pub.port ? `:${pub.port}` : "";
      const urlOut = `${pub.protocol}//${port}-${s.id}.${this.cfg.previewDomain}${portSuffix}/?t=${token}`;
      return json({ token, url: urlOut, expires_in_s: PREVIEW_TTL_MS / 1000 });
    }

    throw notFound("route");
  }

  private createSession(b: CreateSessionBody): Response {
    if (!b.owner_id || typeof b.owner_id !== "string") throw badRequest("owner_id is required");
    if (!b.repo || typeof b.repo !== "string") throw badRequest("repo is required");
    if (typeof b.prompt !== "string") throw badRequest("prompt is required");
    const id = newSessionId();
    const idle = Number(b.idle_timeout_s ?? 1800);
    if (!Number.isFinite(idle) || idle < 60) throw badRequest("idle_timeout_s must be >= 60");
    const row = this.store.insertSession({
      id, owner_id: b.owner_id, repo: b.repo, prompt: b.prompt,
      branch: b.branch?.trim() || `cp/${id}`, base_branch: b.base_branch?.trim() || null,
      image: b.image?.trim() || this.cfg.defaultImage, idle_timeout_s: idle, created_at: Date.now(),
    });
    const env: Record<string, string> = {};
    if (b.secrets?.git_token) env.GIT_TOKEN = b.secrets.git_token;
    if (b.secrets?.anthropic_api_key) env.ANTHROPIC_API_KEY = b.secrets.anthropic_api_key;
    this.sched.submit(row, env);
    return json(this.view(this.store.session(id)!), 201);
  }

  private owned(sid: string, owner_id: string | null) {
    const s = this.store.session(sid);
    if (!s || (owner_id && s.owner_id !== owner_id)) throw notFound("session");
    if (!owner_id) throw badRequest("owner_id is required");
    return s;
  }

  private view(s: ReturnType<Store["session"]> & object) {
    return {
      id: s.id, owner_id: s.owner_id, status: s.status, host_id: s.host_id,
      host_online: s.host_id ? this.hub.isOnline(s.host_id) : null,
      queue_position: s.status === "queued" ? this.sched.queuePosition(s.id) : null,
      ended_reason: s.ended_reason, ended_detail: s.ended_detail,
      repo: s.repo, branch: s.branch, base_branch: s.base_branch, prompt: s.prompt, image: s.image,
      idle_timeout_s: s.idle_timeout_s, created_at: s.created_at, started_at: s.started_at, ended_at: s.ended_at,
    };
  }

  private async dev() {
    // Re-read on every request so edits show up on reload without a restart.
    const html = await Bun.file(new URL("../dev/index.html", import.meta.url)).text();
    return new Response(html, { headers: { "content-type": "text/html; charset=utf-8" } });
  }
}

async function readJson<T>(req: Request): Promise<T> {
  try { return (await req.json()) as T; } catch { throw badRequest("invalid JSON body"); }
}
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });
