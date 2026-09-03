import { expect, test } from "bun:test";
import { Router } from "../src/cp/router.ts";

test("matches by method and segments, captures params, keeps auth flag", () => {
  const r = new Router();
  r.public("GET", "/healthz", () => new Response("ok"));
  r.add("GET", "/sessions/:id", () => new Response("one"));
  r.add("POST", "/sessions/:id/attach-token", () => new Response("tok"));
  expect(r.match("GET", "/healthz")?.auth).toBe(false);
  expect(r.match("POST", "/healthz")).toBeNull();
  const m = r.match("GET", "/sessions/s_abc");
  expect(m?.params).toEqual({ id: "s_abc" });
  expect(m?.auth).toBe(true);
  expect(r.match("POST", "/sessions/s_abc/attach-token")?.params.id).toBe("s_abc");
  expect(r.match("GET", "/sessions/s_abc/attach-token")).toBeNull();
  expect(r.match("GET", "/sessions")).toBeNull();
  expect(r.match("GET", "/sessions/a%20b")?.params.id).toBe("a b");
});
