// A tiny method + path-pattern router. Zero deps by design (decision 16), and
// host-based preview routing has to run before it, which rules out Bun.serve `routes`.
import { badRequest } from "../shared/errors.ts";

export interface Ctx {
  req: Request;
  url: URL;
  params: Record<string, string>;
  /** Parse the JSON body; 400 when it is not valid JSON. */
  json<T = unknown>(): Promise<T>;
}
export type Handler = (ctx: Ctx) => Response | Promise<Response>;

interface Route { method: string; segments: string[]; handler: Handler; auth: boolean }
export interface Match { handler: Handler; params: Record<string, string>; auth: boolean }

export class Router {
  private routes: Route[] = [];

  /** Route behind the service token. */
  add(method: string, pattern: string, handler: Handler) { return this.push(method, pattern, handler, true); }
  /** Route open to anyone (health, token-gated endpoints, dev UI). */
  public(method: string, pattern: string, handler: Handler) { return this.push(method, pattern, handler, false); }

  private push(method: string, pattern: string, handler: Handler, auth: boolean) {
    this.routes.push({ method, segments: split(pattern), handler, auth });
    return this;
  }

  match(method: string, path: string): Match | null {
    const parts = split(path);
    for (const r of this.routes) {
      if (r.method !== method || r.segments.length !== parts.length) continue;
      const params: Record<string, string> = {};
      let ok = true;
      for (let i = 0; i < parts.length && ok; i++) {
        const seg = r.segments[i]!, part = parts[i]!;
        if (seg.startsWith(":")) params[seg.slice(1)] = decodeURIComponent(part);
        else ok = seg === part;
      }
      if (ok) return { handler: r.handler, params, auth: r.auth };
    }
    return null;
  }
}

const split = (p: string) => p.split("/").filter(Boolean);

export function makeCtx(req: Request, url: URL, params: Record<string, string>): Ctx {
  return {
    req, url, params,
    async json<T>() { try { return (await req.json()) as T; } catch { throw badRequest("invalid JSON body"); } },
  };
}
