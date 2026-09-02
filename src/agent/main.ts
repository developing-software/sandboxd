import { loadConfig } from "./config.ts";
import { DockerDriver } from "./docker.ts";
import { SessionManager } from "./sessions.ts";
import { Tunnel } from "./tunnel.ts";
import { logger } from "../shared/log.ts";

const log = logger("agent");
const cfg = loadConfig();
const driver = new DockerDriver(cfg.dockerSock);

// v1 limitation: sandboxes from a previous daemon run can't be re-attached
// (the PTY and ring buffer died with the process), so they are removed.
for (const c of await driver.listManaged()) {
  log.warn("removing orphaned sandbox from previous run", { sid: c.sid });
  await driver.destroy(c.id);
}

let tunnel: Tunnel;
const sessions = new SessionManager(driver, cfg.entry, {
  started: (sid) => tunnel.send({ type: "session.started", sid }),
  ended: (sid, reason, detail) => { tunnel.closePtyStreams(sid); tunnel.send({ type: "session.ended", sid, reason, detail }); },
});
tunnel = new Tunnel(cfg, sessions, driver);

log.info("starting", { name: cfg.name, cp: cfg.cpUrl, max_sessions: cfg.maxSessions, fingerprint: cfg.fingerprint.slice(0, 12) });
tunnel.start();

const shutdown = async () => {
  log.info("shutting down");
  tunnel.stop();
  await sessions.endAll("lost");
  process.exit(0);
};
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
