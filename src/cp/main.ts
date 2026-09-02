import type { ServerWebSocket } from "bun";
import { loadConfig } from "./config.ts";
import { Store } from "./store.ts";
import { Tokens } from "./tokens.ts";
import { HostHub, type TunnelData } from "./tunnel.ts";
import { Scheduler } from "./scheduler.ts";
import { AttachBridge, type AttachData } from "./attach.ts";
import { PreviewProxy } from "./preview.ts";
import { Api } from "./http.ts";
import { logger } from "../shared/log.ts";

const log = logger("cp");
const cfg = loadConfig();
const store = new Store(cfg.dbPath);
const tokens = new Tokens(cfg.secret);

let sched: Scheduler;
const hub = new HostHub(store, {
  hostOnline: (id, running) => sched.onHostOnline(id, running),
  heartbeat: () => sched.onHeartbeat(),
  sessionStarted: (sid) => sched.onSessionStarted(sid),
  sessionEnded: (sid, reason, detail) => sched.onSessionEnded(sid, reason, detail),
});
sched = new Scheduler(store, hub);
sched.boot();

const attach = new AttachBridge(store, hub);
const preview = new PreviewProxy(store, hub, tokens, cfg.previewDomain);
const api = new Api(cfg, store, hub, sched, tokens);

type Data = TunnelData | AttachData;

const server = Bun.serve<Data>({
  port: cfg.port,
  idleTimeout: 255,
  async fetch(req, server) {
    const pv = preview.match(req);
    if (pv) return preview.handle(req, pv);

    const url = new URL(req.url);
    if (url.pathname === "/tunnel") {
      const data: TunnelData = { kind: "tunnel", hostId: null };
      return server.upgrade(req, { data }) ? undefined : new Response("upgrade required", { status: 426 });
    }
    return api.handle(req, server);
  },
  websocket: {
    maxPayloadLength: 16 * 1024 * 1024,
    open(ws: ServerWebSocket<Data>) {
      if (ws.data.kind === "tunnel") hub.onOpen(ws as ServerWebSocket<TunnelData>);
      else attach.onOpen(ws as ServerWebSocket<AttachData>);
    },
    message(ws: ServerWebSocket<Data>, msg) {
      if (ws.data.kind === "tunnel") hub.onMessage(ws as ServerWebSocket<TunnelData>, msg);
      else attach.onMessage(ws as ServerWebSocket<AttachData>, msg);
    },
    close(ws: ServerWebSocket<Data>) {
      if (ws.data.kind === "tunnel") hub.onClose(ws as ServerWebSocket<TunnelData>);
      else attach.onClose(ws as ServerWebSocket<AttachData>);
    },
  },
});

log.info("listening", { url: server.url.toString(), public: cfg.publicUrl, preview: `*.${cfg.previewDomain}`, dev: cfg.dev, db: cfg.dbPath });
