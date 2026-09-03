import { expect, test } from "bun:test";
import { PRESETS, validateEnv } from "../src/cp/presets.ts";
import { loadConfig } from "../src/cp/config.ts";

const cfg = loadConfig({ DEVAGENTS_SERVICE_TOKEN: "t", DEVAGENTS_CODEX_MODEL: "gpt-5", LLM_API_KEY: "k", LLM_BASE_URL: "http://gw/", DEVAGENTS_SANDBOX_ENV_HTTP_PROXY: "http://p" });

test("config: sandbox env from LLM_* shorthands and DEVAGENTS_SANDBOX_ENV_* prefix", () => {
  expect(cfg.sandboxEnv).toEqual({ LLM_API_KEY: "k", LLM_BASE_URL: "http://gw", HTTP_PROXY: "http://p" });
});

test("coding-agent: repo + prompt expand to entry.sh env; secrets split out", () => {
  const x = PRESETS["coding-agent"]!("s_1", {
    repo: "https://x/r.git", prompt: "do it", llm: { base_url: "http://mine/", api_key: "sk" }, secrets: { git_token: "gt" },
  }, cfg);
  expect(x.env).toEqual({ REPO: "https://x/r.git", BRANCH: "devagents/s_1", BASE_BRANCH: "", PROMPT: "do it", AGENT: "claude", MODEL: "claude-sonnet-4-6", LLM_BASE_URL: "http://mine" });
  expect(x.secret_env).toEqual({ LLM_API_KEY: "sk", GIT_TOKEN: "gt" });
});

test("coding-agent: empty prompt -> shell; codex gets its own default model", () => {
  expect(PRESETS["coding-agent"]!("s", { repo: "r" }, cfg).env.AGENT).toBe("shell");
  expect(PRESETS["coding-agent"]!("s", { repo: "r", prompt: "p", agent: "codex" }, cfg).env.MODEL).toBe("gpt-5");
  expect(() => PRESETS["coding-agent"]!("s", { prompt: "p" }, cfg)).toThrow(/repo is required/);
  expect(() => PRESETS["coding-agent"]!("s", { repo: "r", agent: "vim" as any }, cfg)).toThrow(/agent must be/);
});

test("jupyter: defaults image/cmd/idle, validates ui and port, splits the git token", () => {
  const x = PRESETS.jupyter!("s", {}, cfg);
  expect(x.env).toEqual({ JUPYTER_UI: "lab", JUPYTER_PORT: "8888", REPO: "" });
  expect(x.secret_env).toEqual({});
  expect(x.image).toBe("quay.io/jupyter/minimal-notebook:latest");
  expect(x.cmd?.slice(0, 2)).toEqual(["bash", "-lc"]);
  expect(x.cmd?.[2]).toContain("--ip=0.0.0.0");
  expect(x.idle_timeout_s).toBe(4 * 3600);
  const y = PRESETS.jupyter!("s", { repo: "https://x/nb.git", ui: "notebook", port: 9999, secrets: { git_token: "gt" } }, cfg);
  expect(y.env).toEqual({ JUPYTER_UI: "notebook", JUPYTER_PORT: "9999", REPO: "https://x/nb.git" });
  expect(y.secret_env).toEqual({ GIT_TOKEN: "gt" });
  expect(() => PRESETS.jupyter!("s", { ui: "vscode" as any }, cfg)).toThrow(/ui must be/);
  expect(() => PRESETS.jupyter!("s", { port: 70000 }, cfg)).toThrow(/port must be/);
  expect(() => PRESETS.jupyter!("s", { repo: "  " }, cfg)).toThrow(/repo must be/);
});

test("custom: nothing implied", () => {
  expect(PRESETS.custom!("s", {}, cfg)).toEqual({ env: {}, secret_env: {} });
});

test("validateEnv: names, reserved keys, types", () => {
  expect(validateEnv("env", undefined)).toEqual({});
  expect(validateEnv("env", { A_1: "x" })).toEqual({ A_1: "x" });
  expect(() => validateEnv("env", { "bad-name": "x" })).toThrow(/invalid variable name/);
  expect(() => validateEnv("env", { TERM: "x" })).toThrow(/reserved/);
  expect(() => validateEnv("env", { A: 1 })).toThrow(/must be a string/);
  expect(() => validateEnv("env", ["A"])).toThrow(/object/);
});
