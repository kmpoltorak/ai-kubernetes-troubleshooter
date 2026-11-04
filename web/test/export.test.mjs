import assert from "node:assert/strict";
import { after, test } from "node:test";
import { createServer } from "vite";

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: "custom" });
after(() => vite.close());
const { reportJSON, reportMarkdown } = await vite.ssrLoadModule("/src/export.ts");

const incident = { id: "inc-1", title: "checkout | errors", description: "Clients see 503", cluster: "local", namespace: "payments", resource_type: "service", resource_name: "checkout", status: "analyzed", created_at: "2026-01-01T00:00:00Z" };
const meta = { exported_at: "2026-01-02T00:00:00Z", build: { version: "0.1.0", revision: "abc123", built_at: null } };
const base = {
  investigation: { id: "inv-1", incident_id: "inc-1", status: "completed", started_at: "2026-01-01T00:01:00Z", completed_at: "2026-01-01T00:02:00Z" },
  tool_executions: [{ id: "te-1", tool_name: "get_service", status: "succeeded", duration_ms: 3 }, { id: "te-2", tool_name: "get_resource_usage", status: "failed", error: "metrics\nunavailable", duration_ms: 1 }],
  evidence: [{ id: "ev-1", tool_execution_id: "te-1", source: "get_service", subject: "checkout", health: "down", summary: "no endpoints | selector app=checkout", signals: ["SelectorMismatch"] }],
};
const analysis = { summary: "Service selects no pods.", root_cause: "Service selector mismatch", severity: "high", affected_resources: [], possible_causes: ["label typo"], recommended_actions: [], safe_commands: [] };
const actions = [
  { id: "a1", position: 1, kind: "recommendation", description: "Fix the selector", requires_approval: true, status: "proposed" },
  { id: "a2", position: 2, kind: "diagnostic_command", description: "", command: "kubectl get endpoints checkout -n payments", requires_approval: false, status: "proposed" },
];
const v2 = { ...base, report: { created_at: "2026-01-01T00:02:00Z", provider: "rules/rules-v1", remediation_actions: actions, analysis: { ...analysis, schema_version: 2, verdict: "observed_fault", support: "strong", missing_data: ["get_resource_usage failed"], limitations: ["20 pods sampled"], evidence: [{ evidence_id: "ev-1", source: "get_service", description: "no endpoints" }] } } };
const legacy = { ...base, report: { created_at: "2026-01-01T00:02:00Z", provider: "rules/rules-v1", remediation_actions: [], analysis: { ...analysis, confidence: 0.93, evidence: [{ source: "get_service", description: "no endpoints" }] } } };
const failed = { ...base, investigation: { ...base.investigation, status: "failed", error: "provider unavailable" }, report: undefined };

test("v2 Markdown keeps verdict, support, citations, gaps and commands", () => {
  const md = reportMarkdown(incident, v2, meta);
  assert.match(md, /^# checkout \| errors\n/);
  assert.match(md, /schema v2/);
  assert.match(md, /- Verdict: Observed condition/);
  assert.match(md, /- Evidence support: strong \(describes the evidence, not a probability/);
  assert.match(md, /`get_service · ev-1` no endpoints/);
  assert.match(md, /## Missing data\n\n- get_resource_usage failed/);
  assert.match(md, /1\. Fix the selector _\(needs approval\)_/);
  assert.match(md, /```sh\nkubectl get endpoints checkout -n payments\n```/);
  assert.match(md, /app 0\.1\.0 \(abc123\)/);
  // Table cells escape pipes and flatten line breaks.
  assert.match(md, /\| get_service \| down \| no endpoints \\\| selector app=checkout \|/);
  assert.match(md, /\| get_resource_usage \| failed \| metrics unavailable \|/);
});

test("a result without recommendations exports no recommendation section", () => {
  const healthy = { ...v2, report: { ...v2.report, remediation_actions: [], analysis: { ...v2.report.analysis, verdict: "no_fault_observed", support: "limited" } } };
  assert.doesNotMatch(reportMarkdown(incident, healthy, meta), /Recommendations|None reported/);
});

test("legacy Markdown never prints the uncalibrated score", () => {
  const md = reportMarkdown(incident, legacy, meta);
  assert.match(md, /schema v1 \(legacy\)/);
  assert.match(md, /not exported as a number/);
  assert.doesNotMatch(md, /0\.93|93%|Verdict:/);
});

test("a failed attempt exports its error and diagnostics without an assessment", () => {
  const md = reportMarkdown(incident, failed, meta);
  assert.match(md, /- Report: none for this attempt/);
  assert.match(md, /## Error\n\nprovider unavailable/);
  assert.doesNotMatch(md, /## Assessment/);
  assert.match(md, /\| get_service \| down \|/);
});

test("JSON is the API record plus incident and export metadata with the schema version", () => {
  for (const [record, version] of [[v2, 2], [legacy, 1], [failed, null]]) {
    const out = JSON.parse(reportJSON(incident, record, meta));
    assert.deepEqual(out.export, { ...meta, schema_version: version });
    assert.equal(out.incident.id, "inc-1");
    assert.deepEqual(out.investigation, record.investigation);
    assert.deepEqual(out.evidence, record.evidence);
  }
});
