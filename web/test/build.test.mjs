import assert from "node:assert/strict";
import { after, test } from "node:test";
import { createServer as createHTTPServer } from "node:http";
import { once } from "node:events";
import { createServer } from "vite";

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
after(() => vite.close());
const { api } = await vite.ssrLoadModule("/src/api.ts");
const build = { version: "0.1.0-test", revision: "0123456789abcdef0123456789abcdef01234567", built_at: "2026-01-01T12:34:56Z" };

async function fixture(t, handler) {
  const nativeFetch = globalThis.fetch;
  const server = createHTTPServer(handler);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const origin = `http://127.0.0.1:${server.address().port}`;
  globalThis.fetch = (path, options) => nativeFetch(new URL(path, origin), options);
  t.after(async () => { globalThis.fetch = nativeFetch; server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)); });
}

test("readiness keeps build metadata on 503 and accepts an older backend", async (t) => {
  let status = 503;
  let body = { status: "unavailable", checks: { database: "unreachable" }, build: { ...build, ignored_config: "not retained" } };
  await fixture(t, (req, res) => {
    assert.equal(req.url, "/ready");
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(body));
  });
  const signal = new AbortController().signal;
  let result = await api.readiness(signal);
  assert.deepEqual(result.build, build);
  assert.deepEqual(result.checks, { database: "unreachable" });
  status = 200;
  body = { status: "ready", checks: { database: "ok" } };
  result = await api.readiness(signal);
  assert.equal(result.build, undefined);
  assert.deepEqual(result.checks, { database: "ok" });
  for (const bad of [{ ...build, revision: 42 }, { ...build, built_at: "not-a-time" }, { ...build, version: null }]) {
    body.build = bad;
    result = await api.readiness(signal);
    assert.equal(result.build, undefined);
    assert.deepEqual(result.checks, { database: "ok" });
  }
});

test("readiness rejects non-probe responses instead of reporting health", async (t) => {
  let status = 401;
  let body = "{}";
  await fixture(t, (_req, res) => { res.writeHead(status); res.end(body); });
  const signal = new AbortController().signal;
  await assert.rejects(api.readiness(signal), /HTTP 401/);
  status = 200;
  for (const bad of ["not json", "null", "{}", '{"checks":[]}', '{"checks":{"database":42}}']) {
    body = bad;
    await assert.rejects(api.readiness(signal));
  }
  status = 503;
  body = '{"checks":{"database":"ok"}}';
  await assert.rejects(api.readiness(signal), /Invalid readiness response/);
});

test("readiness applies its timeout and respects cancellation", async (t) => {
  await fixture(t, () => {});
  const nativeTimeout = AbortSignal.timeout;
  const budgets = [];
  AbortSignal.timeout = (ms) => { budgets.push(ms); return nativeTimeout(30); };
  t.after(() => { AbortSignal.timeout = nativeTimeout; });
  await assert.rejects(api.readiness(new AbortController().signal), (err) => err.name === "TimeoutError");
  assert.deepEqual(budgets, [15_000]);
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(api.readiness(controller.signal), (err) => err.name === "AbortError");
});
