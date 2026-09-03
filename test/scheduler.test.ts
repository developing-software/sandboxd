import { expect, test } from "bun:test";
import { Store } from "../src/cp/store.ts";
import { Scheduler } from "../src/cp/scheduler.ts";
import type { HostPlacement } from "../src/cp/hosts/hub.ts";
import type { SessionSpec } from "../src/protocol/messages.ts";

class FakeHub implements HostPlacement {
  caps = new Map<string, { running: number; max: number }>();
  created: { hostId: string; spec: SessionSpec }[] = [];
  destroyed: string[] = [];
  isOnline(id: string) { return this.caps.has(id); }
  capacity(id: string) { return this.caps.get(id) ?? null; }
  createSession(hostId: string, spec: SessionSpec) {
    const c = this.caps.get(hostId); if (!c) return false;
    c.running += 1; this.created.push({ hostId, spec }); return true;
  }
  destroySession(_h: string, sid: string) { this.destroyed.push(sid); }
}

function setup(sandboxEnv: Record<string, string> = {}) {
  const store = new Store(":memory:");
  const hub = new FakeHub();
  const sched = new Scheduler(store, hub, sandboxEnv);
  const host = (id: string, max: number) => {
    store.insertPendingHost({ id, name: id, fingerprint: "fp-" + id, approve_code: "AAAA-AA", max_sessions: max });
    store.approveHost(id);
    hub.caps.set(id, { running: 0, max });
  };
  let seq = 0;
  const session = (id: string, env: Record<string, string> = {}) => store.insertSession({
    id, owner_id: "o", preset: "custom", image: "img", cmd: null, env, idle_timeout_s: 60,
    created_at: Date.now() + (seq++),
  });
  return { store, hub, sched, host, session };
}

test("places on the host with most free slots and forwards secrets once", () => {
  const { store, hub, sched, host, session } = setup();
  host("h1", 2); host("h2", 4);
  hub.caps.get("h2")!.running = 3; // h1 free=2, h2 free=1
  sched.submit(session("s_1"), { GIT_TOKEN: "t" });
  expect(hub.created[0]?.hostId).toBe("h1");
  expect(hub.created[0]?.spec.secret_env.GIT_TOKEN).toBe("t");
  expect(store.session("s_1")?.status).toBe("creating");
  sched.sessionStarted("s_1");
  expect(store.session("s_1")?.status).toBe("running");
  expect(store.db.query("SELECT * FROM sessions").all().some((r: any) => JSON.stringify(r).includes("GIT_TOKEN"))).toBe(false);
});

test("queues FIFO when full and drains on session end", () => {
  const { store, hub, sched, host, session } = setup();
  host("h1", 1);
  sched.submit(session("s_1"), {});
  sched.submit(session("s_2"), {});
  sched.submit(session("s_3"), {});
  expect(store.session("s_2")?.status).toBe("queued");
  expect(sched.queuePosition("s_3")).toBe(2);
  hub.caps.get("h1")!.running = 0;
  sched.sessionEnded("s_1", "closed");
  expect(store.session("s_2")?.status).toBe("creating");
  expect(store.session("s_3")?.status).toBe("queued");
  expect(sched.queuePosition("s_3")).toBe(1);
});

test("ignores pending and offline hosts", () => {
  const { store, hub, sched, host, session } = setup();
  store.insertPendingHost({ id: "p", name: "p", fingerprint: "fp", approve_code: "X", max_sessions: 9 });
  hub.caps.set("p", { running: 0, max: 9 });
  host("off", 9); hub.caps.delete("off");
  sched.submit(session("s_1"), {});
  expect(store.session("s_1")?.status).toBe("queued");
});

test("reconciles on host reconnect: unreported sessions are lost", () => {
  const { store, hub, sched, host, session } = setup();
  host("h1", 5);
  sched.submit(session("s_1"), {}); sched.submit(session("s_2"), {});
  sched.sessionStarted("s_1"); sched.sessionStarted("s_2");
  store.markActiveUnknown();
  sched.hostOnline("h1", ["s_2"]);
  expect(store.session("s_1")?.status).toBe("ended");
  expect(store.session("s_1")?.ended_reason).toBe("lost");
  expect(store.session("s_2")?.status).toBe("running");
  expect(store.session("s_2")?.unknown_since).toBeNull();
});

test("boot fails queued sessions (secrets not persisted) and marks active unknown", () => {
  const { store, sched, session } = setup();
  session("s_q");
  sched.boot();
  expect(store.session("s_q")?.status).toBe("ended");
  expect(store.session("s_q")?.ended_reason).toBe("failed");
});

test("cancel: queued -> closed; running -> destroy sent to host", () => {
  const { store, hub, sched, host, session } = setup();
  host("h1", 1);
  sched.submit(session("s_1"), {}); sched.sessionStarted("s_1");
  sched.submit(session("s_2"), {});
  sched.cancel(store.session("s_2")!);
  expect(store.session("s_2")?.status).toBe("ended");
  sched.cancel(store.session("s_1")!);
  expect(hub.destroyed).toEqual(["s_1"]);
  expect(store.session("s_1")?.status).toBe("running"); // ends when the host reports it
});

test("env precedence: session secret_env > session env > operator sandboxEnv; operator env never persisted", () => {
  const { store, hub, sched, host, session } = setup({ LLM_API_KEY: "op-key", LLM_BASE_URL: "http://op", HTTP_PROXY: "http://proxy" });
  host("h1", 1);
  sched.submit(session("s_1", { LLM_BASE_URL: "http://session" }), { LLM_API_KEY: "session-key" });
  const spec = hub.created[0]!.spec;
  expect({ ...spec.env, ...spec.secret_env }).toEqual({ LLM_BASE_URL: "http://session", LLM_API_KEY: "session-key", HTTP_PROXY: "http://proxy" });
  expect(spec.env).toEqual({ LLM_BASE_URL: "http://session" });
  expect(JSON.stringify(store.db.query("SELECT * FROM sessions").all())).not.toContain("op-key");
  expect(JSON.stringify(store.db.query("SELECT * FROM sessions").all())).not.toContain("proxy");
});

test("cmd round-trips as JSON; null means daemon default", () => {
  const { store, hub, sched, host } = setup();
  host("h1", 2);
  sched.submit(store.insertSession({ id: "s_c", owner_id: "o", preset: "custom", image: "img", cmd: ["python3", "-m", "http.server"], env: {}, idle_timeout_s: 60, created_at: 1 }), {});
  sched.submit(store.insertSession({ id: "s_d", owner_id: "o", preset: "custom", image: "img", cmd: null, env: {}, idle_timeout_s: 60, created_at: 2 }), {});
  expect(hub.created[0]?.spec.cmd).toEqual(["python3", "-m", "http.server"]);
  expect(hub.created[1]?.spec.cmd).toBeNull();
});
