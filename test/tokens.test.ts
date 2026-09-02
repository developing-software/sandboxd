import { expect, test } from "bun:test";
import { Tokens } from "../src/cp/tokens.ts";

test("signs and verifies by kind", () => {
  const t = new Tokens("secret");
  const tok = t.sign({ k: "attach", sid: "s_abc" }, 1000);
  expect(t.verify(tok, "attach")?.sid).toBe("s_abc");
  expect(t.verify(tok, "preview")).toBeNull();
});

test("rejects tampering, wrong secret, expiry", () => {
  const t = new Tokens("secret");
  const tok = t.sign({ k: "preview", sid: "s_abc", port: 3000 }, 1000);
  expect(new Tokens("other").verify(tok, "preview")).toBeNull();
  const [body, sig] = tok.split(".");
  expect(t.verify(`${body}x.${sig}`, "preview")).toBeNull();
  expect(t.verify(`${body}.${sig!.slice(0, -1)}A`, "preview")).toBeNull();
  expect(t.verify(t.sign({ k: "preview", sid: "s_abc" }, -1), "preview")).toBeNull();
  expect(t.verify(null, "preview")).toBeNull();
  expect(t.verify("garbage", "preview")).toBeNull();
});
