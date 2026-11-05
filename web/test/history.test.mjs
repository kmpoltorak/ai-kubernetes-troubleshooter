import assert from "node:assert/strict";
import { after, test } from "node:test";
import { createServer } from "vite";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
after(() => vite.close());
const { default: History } = await vite.ssrLoadModule("/src/components/InvestigationHistory.tsx");
const { default: Report } = await vite.ssrLoadModule("/src/components/Report.tsx");
const { default: JobStatus } = await vite.ssrLoadModule("/src/components/JobStatus.tsx");
const render = (Component, props) => renderToStaticMarkup(createElement(Component, props));
const success = { id: "success", status: "completed", started_at: "2026-01-01T00:00:00Z", completed_at: "2026-01-01T00:01:00Z" };
const failure = { id: "failure", status: "failed", started_at: "2026-01-02T00:00:00Z", completed_at: "2026-01-02T00:01:00Z", error: "provider unavailable" };
const latestReport = { investigation: success, report: { created_at: "2026-01-01T00:01:00Z" } };
const props = { page: { investigations: [failure, success], offset: 0, limit: 20, has_more: false }, selectedID: "failure", latestReport, busy: false, error: "", onSelect() {}, onPage() {}, onRefresh() {} };

test("history distinguishes latest failed attempt from older successful report", () => {
  const html = render(History, props);
  assert.match(html, /Latest successful report:.*2026-01-01T00:01:00Z/);
  assert.match(html, /failed<\/strong><span> · Latest attempt<\/span>/);
  assert.match(html, /aria-pressed="true"[^>]*><strong>failed/);
  assert.match(html, /aria-pressed="false"[^>]*><strong>completed/);
  assert.match(html, /datetime="2026-01-02T00:00:00Z"/i);
});

test("empty and older pages show their pagination state", () => {
  const html = render(History, { ...props, latestReport: null, page: { ...props.page, investigations: [], offset: 20, has_more: true } });
  assert.match(html, /No investigations on this page/);
  assert.match(html, /Page 2/);
  assert.doesNotMatch(html, /Latest attempt/);
  assert.doesNotMatch(html, /disabled=""[^>]*>Older attempts/);
  const first = render(History, props);
  assert.match(first, /disabled=""[^>]*>Newer attempts/);
  assert.match(first, /disabled=""[^>]*>Older attempts/);
});

test("failed details retain evidence and error without showing a root cause report", () => {
  const html = render(Report, { record: { investigation: failure, report: null,
    tool_executions: [{ id: "tool", tool_name: "get_pods", duration_ms: 12 }],
    evidence: [{ tool_execution_id: "tool", subject: "pod/api", health: "down", summary: "OOMKilled", signals: ["OOMKilled"] }],
  } });
  assert.match(html, /Selected investigation/);
  assert.match(html, /provider unavailable/);
  assert.match(html, /2026-01-02T00:01:00Z/);
  assert.match(html, /OOMKilled/);
  assert.doesNotMatch(html, /Root cause/);
});

test("running and legacy null collections remain readable; errors are escaped", () => {
  const html = render(Report, { record: { investigation: { ...failure, status: "running", completed_at: undefined, error: "<script>alert(1)</script>" }, tool_executions: null, evidence: null } });
  assert.match(html, /This investigation is running/);
  assert.match(html, /saved diagnostic results/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
});

test("queued attempts remain readable before a worker starts", () => {
  const html = render(Report, { record: { investigation: { ...failure, status: "queued", error: undefined, completed_at: undefined }, tool_executions: [], evidence: [] } });
  assert.match(html, /This investigation is queued/);
  assert.doesNotMatch(html, /Root cause/);
});

test("job status shows queue, analysis and unknown outcome without hiding stored results", () => {
  const callbacks = { onRetry() {}, onView() {} };
  for (const stage of ["queued", "diagnostics", "analyzing"]) {
    const status = stage === "queued" ? "queued" : "running";
    const html = render(JobStatus, { ...callbacks, record: { job: { id: "job", status, stage, attempts: 2 } }, error: "offline" });
    assert.match(html, new RegExp(`Stage: <strong>${stage}`));
    assert.match(html, /Attempt 2 of 3/);
    assert.match(html, /Checking every 2 seconds/);
    assert.match(html, /Leaving this page does not stop/);
    assert.match(html, /Saved results remain below/);
    assert.match(html, /Retry status check/);
  }
  const html = render(JobStatus, { ...callbacks, record: { job: { id: "job", status: "failed", stage: "failed", attempts: 1, error: "analysis outcome unknown; start a new job to retry" } }, error: "" });
  assert.match(html, /analysis outcome unknown/);
  assert.doesNotMatch(html, /Checking every/);
});

test("v2 assessment links exact evidence, separates hypotheses and states bounds without a percentage", () => {
  const item = { id: "ev-one", tool_execution_id: "tool", source: "get_pod_logs", subject: "pod/api", health: "degraded", summary: "connection timeout", signals: [] };
  const record = { investigation: success, tool_executions: [{ id: "tool", tool_name: "get_pod_logs", duration_ms: 2 }], evidence: [item], report: { provider: "rules/rules-v2", created_at: success.completed_at, analysis: { schema_version: 2, verdict: "suspected_fault", support: "limited", summary: "Timeout plus declarations do not prove enforcement", root_cause: "Possible NetworkPolicy restriction", severity: "high", evidence: [{ evidence_id: item.id, source: item.source, description: "timeout" }], missing_data: ["metrics unavailable"], limitations: ["CNI enforcement not observed", "sampled logs only"], possible_causes: ["dependency unavailable"] } } };
  const html = render(Report, { record });
  assert.match(html, /Assessment/);
  assert.match(html, /Hypothesis/);
  assert.match(html, /Evidence support: <strong>limited/);
  assert.match(html, /Possible causes \(hypotheses\)/);
  assert.match(html, /href="#evidence-success-ev-one"/);
  assert.match(html, /id="evidence-success-ev-one"/);
  assert.match(html, /metrics unavailable/);
  assert.match(html, /CNI enforcement not observed/);
  assert.match(html, /sampled logs only/);
  assert.doesNotMatch(html, /Confidence|\d+%/);
  const other = render(Report, { record: { ...record, investigation: { ...success, id: "other-attempt" }, evidence: [] } });
  assert.doesNotMatch(other, /href="#evidence/);
});

test("legacy reports stay readable without recasting their score as evidence support", () => {
  const html = render(Report, { record: { investigation: success, evidence: [], tool_executions: [], report: { provider: "rules/rules-v1", analysis: { summary: "old summary", root_cause: "old cause", confidence: 0.94, severity: "high", evidence: [{ source: "get_pods", description: "legacy citation" }] } } } });
  assert.match(html, /old summary/);
  assert.match(html, /legacy citation/);
  assert.match(html, /Legacy report/);
  assert.doesNotMatch(html, /94%|Evidence support:|href="#evidence/);
});

test("a report without recommendations or commands shows no remediation card", () => {
  const record = { investigation: success, tool_executions: [], evidence: [], report: { created_at: "2026-01-01T00:01:00Z", provider: "rules/rules-v2", remediation_actions: [],
    analysis: { schema_version: 2, verdict: "no_fault_observed", support: "limited", summary: "s", root_cause: "No Kubernetes-level fault detected", severity: "low", affected_resources: [], evidence: [], possible_causes: [], recommended_actions: [], safe_commands: [], missing_data: [], limitations: [] } } };
  const html = render(Report, { record });
  assert.match(html, /No Kubernetes-level fault detected/);
  assert.doesNotMatch(html, /Remediation|needs approval|Read-only commands/);
  const withCommand = { ...record, report: { ...record.report, remediation_actions: [{ id: "c", position: 1, kind: "diagnostic_command", description: "", command: "kubectl get pods -n payments", requires_approval: false, status: "proposed" }] } };
  const commandOnly = render(Report, { record: withCommand });
  assert.match(commandOnly, /Read-only commands/);
  assert.doesNotMatch(commandOnly, />Remediation</);
});

test("a report shows no noise: approval badges, durations, repeated signals, raw IDs", () => {
  const item = { id: "ev-pods", tool_execution_id: "tool", source: "get_pods", subject: "pods/app=api", health: "down", summary: "0/1 pods ready. Findings: CrashLoopBackOff.", signals: ["CrashLoopBackOff", "FrequentRestarts"] };
  const record = { investigation: success, tool_executions: [{ id: "tool", tool_name: "get_pods", duration_ms: 1234 }], evidence: [item],
    report: { provider: "rules/rules-v2", created_at: success.completed_at, remediation_actions: [{ id: "r", position: 1, kind: "recommendation", description: "fix the config", requires_approval: true, status: "proposed" }],
      analysis: { schema_version: 2, verdict: "observed_fault", support: "strong", summary: "s", root_cause: "Crash", severity: "high", evidence: [], missing_data: [], limitations: [], possible_causes: ["x"] } } };
  const html = render(Report, { record });
  assert.match(html, /fix the config/);
  assert.doesNotMatch(html, /needs approval|1234 ms|Duration|coverage is still limited|Status: <strong/);
  // The summary already names CrashLoopBackOff; only the unnamed signal gets a chip.
  assert.equal(html.match(/>CrashLoopBackOff</g), null);
  assert.match(html, />FrequentRestarts</);
  const history = render(History, props);
  assert.doesNotMatch(history, />success<|>failure</);
  assert.match(history, /title="Investigation success"/);
  const job = render(JobStatus, { onRetry() {}, onView() {}, error: "", record: { job: { id: "job-123", status: "completed", stage: "completed", attempts: 1 } } });
  assert.doesNotMatch(job, />job-123<|Stage:|Attempt 1 of 3/);
});
