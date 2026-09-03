import { expect, test } from "bun:test";
import { loadConfig } from "../src/cp/config.ts";
import { PresetRegistry, builtinPresets } from "../src/cp/presets/index.ts";
import { codingAgentPreset } from "../src/cp/presets/coding-agent.ts";
import { jupyterPreset } from "../src/cp/presets/jupyter.ts";
import { customPreset } from "../src/cp/presets/custom.ts";

const env = { DEVAGENTS_SERVICE_TOKEN: "t", DEVAGENTS_CODEX_MODEL: "gpt-5", LLM_API_KEY: "k", LLM_BASE_URL: "http://gw/", DEVAGENTS_SANDBOX_ENV_HTTP_PROXY: "http://p" };
const coding = codingAgentPreset(env);
const jupyter = jupyterPreset(env);

test("config: sandbox env from LLM_* shorthands and DEVAGENTS_SANDBOX_ENV_* prefix", () => {
  expect(loadConfig(env).sandboxEnv).toEqual({ LLM_API_KEY: "k", LLM_BASE_URL: "http://gw", HTTP_PROXY: "http://p" });
});

test("coding-agent: repo + prompt expand to entry.sh env; secrets split out", () => {
  const x = coding.expand("s_1", {
    repo: "https://x/r.git", prompt: "do it", llm: { base_url: "http://mine/", api_key: "sk" }, secrets: { git_token: "gt" },
  });
  expect(x.env).toEqual({ REPO: "https://x/r.git", BRANCH: "devagents/s_1", BASE_BRANCH: "", PROMPT: "do it", AGENT: "claude", MODEL: "claude-sonnet-4-6", SETUP: "", LLM_BASE_URL: "http://mine" });
  expect(x.secret_env).toEqual({ LLM_API_KEY: "sk", GIT_TOKEN: "gt" });
});

test("coding-agent: empty prompt -> shell; codex gets its own default model", () => {
  expect(coding.expand("s", { repo: "r" }).env.AGENT).toBe("shell");
  expect(coding.expand("s", { repo: "r", prompt: "p", agent: "codex" }).env.MODEL).toBe("gpt-5");
  expect(() => coding.expand("s", { prompt: "p" })).toThrow(/repo is required/);
  expect(() => coding.expand("s", { repo: "r", agent: "vim" })).toThrow(/agent must be/);
});

test("jupyter: defaults image/cmd/idle, validates ui and port, splits the git token", () => {
  const x = jupyter.expand("s", {});
  expect(x.env).toEqual({ JUPYTER_UI: "lab", JUPYTER_PORT: "8888", REPO: "" });
  expect(x.secret_env).toEqual({});
  expect(x.image).toBe("quay.io/jupyter/minimal-notebook:latest");
  expect(x.cmd?.slice(0, 2)).toEqual(["bash", "-lc"]);
  expect(x.cmd?.[2]).toContain("--ip=0.0.0.0");
  expect(x.idle_timeout_s).toBe(4 * 3600);
  const y = jupyter.expand("s", { repo: "https://x/nb.git", ui: "notebook", port: 9999, secrets: { git_token: "gt" } });
  expect(y.env).toEqual({ JUPYTER_UI: "notebook", JUPYTER_PORT: "9999", REPO: "https://x/nb.git" });
  expect(y.secret_env).toEqual({ GIT_TOKEN: "gt" });
  expect(() => jupyter.expand("s", { ui: "vscode" })).toThrow(/ui must be/);
  expect(() => jupyter.expand("s", { port: 70000 })).toThrow(/port must be/);
  expect(() => jupyter.expand("s", { repo: "  " })).toThrow(/repo must be/);
});

test("custom: nothing implied", () => {
  expect(customPreset().expand("s", {})).toEqual({ env: {}, secret_env: {} });
});

test("registry: explicit name, claim by repo, fallback to custom, unknown -> 400", () => {
  const reg = new PresetRegistry(builtinPresets(env));
  expect(reg.names).toEqual(["coding-agent", "jupyter", "custom"]);
  expect(reg.resolve({ preset: "jupyter", repo: "r" }).name).toBe("jupyter");
  expect(reg.resolve({ repo: "r" }).name).toBe("coding-agent");
  expect(reg.resolve({ image: "python:3.12" }).name).toBe("custom");
  expect(() => reg.resolve({ preset: "nope" })).toThrow(/preset must be one of/);
  expect(() => new PresetRegistry([customPreset()], "missing")).toThrow(/not registered/);
});

test("coding-agent: setup command is passed through as SETUP", () => {
  expect(coding.expand("s", { repo: "r", setup: " bun install " }).env.SETUP).toBe("bun install");
  expect(coding.expand("s", { repo: "r" }).env.SETUP).toBe("");
  expect(() => coding.expand("s", { repo: "r", setup: 1 })).toThrow(/setup must be a string/);
});
