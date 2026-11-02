import assert from "node:assert/strict";
import { after, beforeEach, test } from "node:test";
import { createServer as createHTTPServer } from "node:http";
import { once } from "node:events";
import { createServer } from "vite";

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
after(() => vite.close());
const jobs = await vite.ssrLoadModule("/src/jobs.ts");
const id = "f512e621-1258-4d29-8bf1-7029b121cb45";
const jobID = "27dad2df-0f65-4daa-99f9-d8a64519a2ac";
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const record = (status, attempt = 1) => ({ job: { id: jobID, incident_id: id, status, stage: status === "running" ? "diagnostics" : status, attempts: attempt }, result: { investigation: { id: `attempt-${attempt}`, job_id: jobID, status }, tool_executions: [], evidence: [{ summary: "durable evidence" }] } });
const values = new Map();
const storage = { getItem: (key) => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) };
Object.defineProperty(globalThis, "sessionStorage", { value: storage, configurable: true });
beforeEach(() => values.clear());

async function fixture(t, handler) {
  const nativeFetch = globalThis.fetch;
  const requests = [];
  const server = createHTTPServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    requests.push({ method: req.method, path: req.url, key: req.headers["idempotency-key"], body, at: Date.now() });
    res.setHeader("Content-Type", "application/json");
    await handler(req, res, requests.length);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const origin = `http://127.0.0.1:${server.address().port}`;
  globalThis.fetch = (path, options) => nativeFetch(new URL(path, origin), options);
  t.after(async () => { globalThis.fetch = nativeFetch; server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)); });
  return requests;
}

const controller = (t) => { const c = new AbortController(); t.after(() => c.abort()); return c; };

test("an uncertain submission survives reload with the same UUID and options", async (t) => {
  const requests = await fixture(t, (req, res, count) => {
    assert.equal(jobs.readSession(id).key, req.headers["idempotency-key"], "key must be durable before HTTP");
    if (count === 1) { res.destroy(); return; }
    res.statusCode = 202;
    res.end(JSON.stringify(record("queued")));
  });
  const c = controller(t);
  await assert.rejects(jobs.submitInvestigation(id, "oom_killed", c.signal));
  const saved = jobs.readSession(id);
  assert.match(saved.key, uuid);
  assert.equal(saved.scenario, "oom_killed");
  // Re-import like a fresh page: no in-memory pending request is retained.
  vite.moduleGraph.invalidateAll();
  const fresh = await vite.ssrLoadModule("/src/jobs.ts");
  const result = await fresh.submitInvestigation(id, "healthy", c.signal);
  assert.equal(result.job.id, jobID);
  assert.equal(requests.length, 2);
  assert.equal(requests[1].key, requests[0].key);
  assert.equal(requests[1].body, '{"scenario":"oom_killed"}');
  assert.equal(requests[1].path, `/api/v1/incidents/${id}/jobs`);
  assert.deepEqual(jobs.readSession(id), { jobID });
  assert.doesNotMatch([...values.values()].join(""), /durable evidence/);
});

test("a deliberate new analysis uses a new key; rejected requests allow editing", async (t) => {
  const requests = await fixture(t, (_req, res, count) => {
    res.statusCode = count === 1 ? 429 : 202;
    res.end(JSON.stringify(count === 1 ? { error: { message: "queue full" } } : record("completed")));
  });
  const c = controller(t);
  await assert.rejects(jobs.submitInvestigation(id, "healthy", c.signal), /queue full/);
  assert.equal(jobs.readSession(id), null);
  await jobs.submitInvestigation(id, "oom_killed", c.signal);
  await jobs.submitInvestigation(id, "healthy", c.signal);
  assert.equal(new Set(requests.map((r) => r.key)).size, 3);
  assert.equal(requests[1].body, '{"scenario":"oom_killed"}');
});

test("503 retains an uncertain submission, and storage failure sends nothing", async (t) => {
  const requests = await fixture(t, (_req, res) => { res.statusCode = 503; res.end('{"error":{"message":"unavailable"}}'); });
  const c = controller(t);
  await assert.rejects(jobs.submitInvestigation(id, "healthy", c.signal), /unavailable/);
  const saved = jobs.readSession(id);
  await assert.rejects(jobs.submitInvestigation(id, "oom_killed", c.signal));
  assert.equal(requests[0].key, requests[1].key);
  assert.equal(requests[1].body, '{"scenario":"healthy"}');
  assert.deepEqual(jobs.readSession(id), saved);
  const originalSet = storage.setItem;
  storage.setItem = () => { throw new Error("session storage unavailable"); };
  t.after(() => { storage.setItem = originalSet; });
  await assert.rejects(jobs.submitInvestigation(id, "healthy", c.signal), /session storage unavailable/);
  assert.equal(requests.length, 2);
});

test("malformed session data blocks submission without destroying the original", async (t) => {
  const requests = await fixture(t, (_req, res) => res.end("{}"));
  const c = controller(t);
  for (const bad of ['{"key":"not-a-uuid","scenario":"healthy"}', '{"jobID":"../other"}', "invalid json"]) {
    values.set(`investigation-job:${id}`, bad);
    await assert.rejects(jobs.submitInvestigation(id, "healthy", c.signal));
    assert.equal(values.get(`investigation-job:${id}`), bad);
  }
  assert.equal(requests.length, 0);
});

test("polling resumes a saved job, follows retry attempts, then stops at failure", async (t) => {
  const requests = await fixture(t, (_req, res, count) => res.end(JSON.stringify(record(count < 3 ? "running" : "failed", count < 2 ? 1 : 2))));
  jobs.saveSession(id, { jobID });
  const c = controller(t);
  const updates = [];
  await jobs.followJob(id, jobs.readSession(id).jobID, c.signal, (r) => updates.push(r), (e) => assert.fail(e));
  assert.equal(requests.length, 3);
  assert.ok(requests.every((r) => r.method === "GET" && r.path.endsWith(`/jobs/${jobID}`)));
  assert.ok(requests[1].at - requests[0].at >= 1900, "must wait two seconds between reads");
  assert.deepEqual(updates.map((r) => r.result.investigation.id), ["attempt-1", "attempt-2", "attempt-2"]);
  assert.equal(updates.at(-1).result.evidence[0].summary, "durable evidence");
  assert.equal(updates.at(-1).job.status, "failed");
});

test("temporary read failure retains evidence and retries only GET", async (t) => {
  const requests = await fixture(t, (_req, res, count) => {
    res.statusCode = count === 2 ? 503 : 200;
    res.end(JSON.stringify(count === 2 ? { error: { message: "offline" } } : record(count === 1 ? "running" : "completed")));
  });
  const c = controller(t);
  let latest;
  const errors = [];
  await jobs.followJob(id, jobID, c.signal, (r) => { latest = r; }, (e) => { errors.push(e.message); assert.equal(latest.result.evidence[0].summary, "durable evidence"); });
  assert.deepEqual(errors, ["offline"]);
  assert.equal(latest.job.status, "completed");
  assert.ok(requests.every((r) => r.method === "GET"));
});

test("leaving the view cancels an in-flight read and leaves saved ownership", async (t) => {
  let reached;
  const started = new Promise((resolve) => { reached = resolve; });
  const requests = await fixture(t, () => reached());
  jobs.saveSession(id, { jobID });
  const c = controller(t);
  const done = jobs.followJob(id, jobID, c.signal, () => assert.fail("late update"), () => assert.fail("abort is not an error"));
  await started;
  c.abort();
  await done;
  assert.equal(requests.length, 1);
  assert.deepEqual(jobs.readSession(id), { jobID });
});

test("abort during poll delay stops immediately; authentication errors are terminal", async (t) => {
  const requests = await fixture(t, (_req, res, count) => {
    res.statusCode = count === 1 ? 200 : 401;
    res.end(JSON.stringify(count === 1 ? record("running") : { error: { message: "login required" } }));
  });
  const c = controller(t);
  const done = jobs.followJob(id, jobID, c.signal, () => { setTimeout(() => c.abort(), 20); }, () => assert.fail("unexpected error"));
  await done;
  assert.equal(requests.length, 1);
  const errors = [];
  await jobs.followJob(id, jobID, controller(t).signal, () => assert.fail("unexpected update"), (e) => errors.push(e.message));
  assert.deepEqual(errors, ["login required"]);
  assert.equal(requests.length, 2);
});

test("a stalled submission times out with its key available for replay", { timeout: 20_000 }, async (t) => {
  const requests = await fixture(t, () => {});
  await assert.rejects(jobs.submitInvestigation(id, "healthy", controller(t).signal), (e) => e.name === "TimeoutError");
  assert.equal(requests.length, 1);
  assert.equal(jobs.readSession(id).key, requests[0].key);
});

test("an unreadable successful response remains an uncertain submission", async (t) => {
  const requests = await fixture(t, (_req, res) => res.end("<html>login page</html>"));
  await assert.rejects(jobs.submitInvestigation(id, "healthy", controller(t).signal));
  assert.equal(jobs.readSession(id).key, requests[0].key);
});
