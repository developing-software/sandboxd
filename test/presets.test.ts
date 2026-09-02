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
