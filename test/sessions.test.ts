import { expect, test } from "bun:test";
import { Store } from "../src/cp/store.ts";
import { Scheduler } from "../src/cp/scheduler.ts";
import { Tokens } from "../src/cp/tokens.ts";
import { PresetRegistry, builtinPresets } from "../src/cp/presets/index.ts";
import { SessionService, validateEnv } from "../src/cp/sessions.ts";
import type { HostPlacement } from "../src/cp/hosts/hub.ts";
import type { SessionSpec } from "../src/protocol/messages.ts";

class NoHosts implements HostPlacement {
  isOnline() { return false; }
  capacity() { return null; }
  createSession(_h: string, _s: SessionSpec) { return false; }
  destroySession() {}
}

function setup() {
  const store = new Store(":memory:");
  const sched = new Scheduler(store, new NoHosts());
  const cfg = { defaultImage: "default:latest", publicUrl: "https://cp.example.com", previewDomain: "preview.example.com" };
  const presets = new PresetRegistry(builtinPresets({ DEVAGENTS_JUPYTER_IMAGE: "jup:1" }));
  return { store, svc: new SessionService(cfg, store, new NoHosts(), sched, new Tokens("k"), presets) };
}

test("create: preset picked by repo, caller values win, secrets never stored", () => {
  const { store, svc } = setup();
  const v = svc.create({ owner_id: "me", repo: "https://x/r.git", prompt: "p", env: { EXTRA: "1", PROMPT: "override" }, secrets: { git_token: "gt" }, secret_env: { S: "x" } });
  expect(v.preset).toBe("coding-agent");
  expect(v.status).toBe("queued");
  expect(v.queue_position).toBe(1);
  expect(v.image).toBe("default:latest");
  expect(v.env.PROMPT).toBe("override");
  expect(v.env.EXTRA).toBe("1");
  expect(v.cmd).toBeNull();
  const raw = JSON.stringify(store.db.query("SELECT * FROM sessions").all());
  expect(raw).not.toContain("gt");
  expect(raw).not.toContain('"S"');
});

test("create: preset defaults for image/cmd/idle apply unless the caller overrides", () => {
  const { svc } = setup();
  const j = svc.create({ owner_id: "me", preset: "jupyter" });
  expect(j.image).toBe("jup:1");
  expect(j.cmd?.[0]).toBe("bash");
  expect(j.idle_timeout_s).toBe(4 * 3600);
  const c = svc.create({ owner_id: "me", preset: "jupyter", image: "mine", cmd: ["sh"], idle_timeout_s: 120 });
  expect([c.image, c.cmd, c.idle_timeout_s]).toEqual(["mine", ["sh"], 120]);
});

test("create: validation errors are 400s", () => {
  const { svc } = setup();
  expect(() => svc.create({})).toThrow(/owner_id is required/);
  expect(() => svc.create({ owner_id: "me", cmd: [] })).toThrow(/cmd must be/);
  expect(() => svc.create({ owner_id: "me", idle_timeout_s: 5 })).toThrow(/idle_timeout_s/);
  expect(() => svc.create({ owner_id: "me", preset: "nope" })).toThrow(/preset must be one of/);
  expect(() => svc.create({ owner_id: "me", env: { TERM: "x" } })).toThrow(/reserved/);
  expect(() => svc.create("nope")).toThrow(/JSON object/);
});

test("ownership: another owner sees 404, missing owner_id is 400; cancel and tokens", () => {
  const { svc } = setup();
  const v = svc.create({ owner_id: "me" });
  expect(svc.get(v.id, "me").id).toBe(v.id);
  expect(() => svc.get(v.id, "you")).toThrow(/session not found/);
  expect(() => svc.get(v.id, null)).toThrow(/owner_id is required/);
  expect(() => svc.attachToken(v.id, "me")).toThrow(/session is queued/);
  const p = svc.previewToken(v.id, "me", 3000);
  expect(p.url).toBe(`https://3000-${v.id}.preview.example.com/?t=${p.token}`);
  expect(() => svc.previewToken(v.id, "me", 0)).toThrow(/port must be/);
  expect(svc.cancel(v.id, "me").status).toBe("ended");
  expect(svc.list("me")).toHaveLength(1);
  expect(svc.list("you")).toHaveLength(0);
});

test("validateEnv: names, reserved keys, types", () => {
  expect(validateEnv("env", undefined)).toEqual({});
  expect(validateEnv("env", { A_1: "x" })).toEqual({ A_1: "x" });
  expect(() => validateEnv("env", { "bad-name": "x" })).toThrow(/invalid variable name/);
  expect(() => validateEnv("env", { TERM: "x" })).toThrow(/reserved/);
  expect(() => validateEnv("env", { A: 1 })).toThrow(/must be a string/);
  expect(() => validateEnv("env", ["A"])).toThrow(/object/);
});
