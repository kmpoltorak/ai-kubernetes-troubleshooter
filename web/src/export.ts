import type { BuildInfo, Incident, InvestigationRecord } from "./api";
import { target } from "./format";

// Exports contain only what the API returned, which is masked server-side.
export interface ExportMeta {
  exported_at: string;
  build: BuildInfo | null;
}

export const verdictLabel: Record<string, string> = {
  observed_fault: "Observed condition", suspected_fault: "Hypothesis",
  no_fault_observed: "No fault observed in this sample", inconclusive: "Inconclusive",
};

// Shown with every report a language model wrote; the rules analyzer is deterministic.
export const aiNotice = (provider: string) =>
  provider.startsWith("rules/") ? null : `Written by ${provider}. AI can make mistakes: verify the findings and commands against the cited evidence before acting.`;

// Reports without schema_version are the legacy v1 contract.
export const schemaVersion = (record: InvestigationRecord) =>
  record.report ? record.report.analysis.schema_version ?? 1 : null;

export function reportJSON(incident: Incident, record: InvestigationRecord, meta: ExportMeta): string {
  const notice = record.report && aiNotice(record.report.provider);
  return JSON.stringify({ export: { ...meta, schema_version: schemaVersion(record), ...(notice && { ai_notice: notice }) }, incident, ...record }, null, 2) + "\n";
}

const cell = (s: string) => s.replace(/\|/g, "\\|").replace(/\s*\n\s*/g, " ");
const list = (items: string[] | undefined) => (items?.length ? items.map((i) => `- ${i}`).join("\n") : "_None reported._");

export function reportMarkdown(incident: Incident, record: InvestigationRecord, meta: ExportMeta): string {
  const inv = record.investigation;
  const report = record.report;
  const a = report?.analysis;
  const version = schemaVersion(record);
  const out = [
    `# ${incident.title}`,
    "",
    `- Target: \`${target(incident)}\``,
    `- Incident: \`${incident.id}\``,
    `- Investigation: \`${inv.id}\` · ${inv.status} · started ${inv.started_at}${inv.completed_at ? ` · finished ${inv.completed_at}` : ""}`,
    report ? `- Report: ${report.created_at} · analyzer \`${report.provider}\` · schema ${version === 1 ? "v1 (legacy)" : `v${version}`}` : "- Report: none for this attempt",
    `- Exported: ${meta.exported_at}${meta.build ? ` · app ${meta.build.version} (${meta.build.revision})` : ""}`,
  ];
  if (incident.description) out.push("", "## Description", "", incident.description);
  if (inv.error) out.push("", "## Error", "", inv.error);
  if (a && report) {
    out.push("", "## Assessment", "");
    const notice = aiNotice(report.provider);
    if (notice) out.push(`> ${notice}`, "");
    out.push(`**${a.root_cause}**`, "", a.summary, "", `- Severity: ${a.severity}`);
    if (version === 2) {
      out.push(`- Verdict: ${a.verdict ? verdictLabel[a.verdict] ?? a.verdict : "unknown"}`,
        `- Evidence support: ${a.support} (describes the evidence, not a probability of correctness)`);
    } else {
      out.push("- Legacy report: tool-level citations and an uncalibrated score, which is not exported as a number.");
    }
    out.push("", "## Cited evidence", "", list((a.evidence ?? []).map((e) => `\`${e.source}${e.evidence_id ? ` · ${e.evidence_id}` : ""}\` ${e.description}`)));
    out.push("", "## Possible causes (hypotheses)", "", list(a.possible_causes));
    if (version === 2) out.push("", "## Missing data", "", list(a.missing_data), "", "## Scope and sampling limits", "", list(a.limitations));
    const actions = report.remediation_actions ?? [];
    const recs = actions.filter((r) => r.kind === "recommendation");
    const commands = actions.filter((r) => r.kind === "diagnostic_command" && r.command);
    if (recs.length) {
      out.push("", "## Recommendations (require human review)", "",
        recs.map((r) => `${r.position}. ${r.description}`).join("\n"));
    }
    if (commands.length) out.push("", "## Read-only commands", "", "```sh", ...commands.map((c) => c.command ?? ""), "```");
  }
  const evidence = new Map((record.evidence ?? []).map((e) => [e.tool_execution_id, e]));
  out.push("", "## Diagnostics", "", "| Tool | Result | Observation |", "|---|---|---|");
  for (const te of record.tool_executions ?? []) {
    const ev = evidence.get(te.id);
    out.push(`| ${te.tool_name} | ${ev?.health ?? "failed"} | ${cell(ev?.summary ?? te.error ?? "")} |`);
  }
  return out.join("\n") + "\n";
}

export function download(name: string, type: string, body: string) {
  const url = URL.createObjectURL(new Blob([body], { type }));
  const link = Object.assign(document.createElement("a"), { href: url, download: name });
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
