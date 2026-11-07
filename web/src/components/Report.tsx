import { useState, type ReactNode } from "react";
import type { Health, InvestigationRecord, Severity } from "../api";
import { aiNotice, verdictLabel } from "../export";
import { timeAgo } from "../format";
import { Hint } from "./Hint";

// A section heading with an explanation behind "?".
function Titled({ title, hint, className = "" }: { title: string; hint: ReactNode; className?: string }) {
  return (
    <div className={`flex flex-wrap items-center gap-1.5 ${className}`}>
      <h4 className="section-title mb-0">{title}</h4>
      <Hint topic={title}>{hint}</Hint>
    </div>
  );
}

const severityStyle: Record<Severity, string> = {
  low: "text-emerald-700 border-emerald-500/40 dark:text-emerald-400",
  medium: "text-amber-700 border-amber-500/40 dark:text-amber-400",
  high: "text-red-600 border-red-500/40 dark:text-red-400",
  critical: "text-fuchsia-600 border-fuchsia-500/40 dark:text-fuchsia-400",
};

const verdictStyle: Record<string, string> = {
  observed_fault: "bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300",
  suspected_fault: "bg-amber-50 text-amber-800 dark:bg-amber-500/10 dark:text-amber-300",
  no_fault_observed: "bg-emerald-50 text-emerald-800 dark:bg-emerald-500/10 dark:text-emerald-300",
  inconclusive: "bg-zinc-100 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300",
};

const healthStyle: Record<Health | "failed", string> = {
  healthy: "text-emerald-700 dark:text-emerald-400",
  degraded: "text-amber-700 dark:text-amber-400",
  down: "text-red-600 dark:text-red-400",
  failed: "text-red-600 dark:text-red-400",
};

const chip = "rounded-md border border-zinc-200 px-1.5 py-0.5 font-mono text-[11px] dark:border-zinc-700";
const muted = "text-zinc-600 dark:text-zinc-400";

interface Props {
  record: InvestigationRecord;
  /** Actions for this attempt (export), shown in the card's header. */
  toolbar?: ReactNode;
  /** A notice above the conclusion, e.g. that this attempt is not the newest. */
  notice?: ReactNode;
}

// Conclusion first; next steps; then hypotheses, limits and raw diagnostics, collapsed.
export default function Report({ record, toolbar, notice }: Props) {
  const inv = record.investigation;
  const report = record.report;
  const a = report?.analysis;
  // The API returns [] for collections, but older stored records may hold null.
  const evidence = record.evidence ?? [];
  const executions = record.tool_executions ?? [];
  const evidenceByExec = new Map(evidence.map((e) => [e.tool_execution_id, e]));
  const v2 = a?.schema_version === 2;
  const citedIDs = new Set(evidence.map((e) => e.id));
  const anchor = (id: string) => `evidence-${inv.id}-${id}`;
  const diagnosticsID = `diagnostics-${inv.id}`;
  const actions = report?.remediation_actions ?? [];
  const recommendations = actions.filter((r) => r.kind === "recommendation");
  const commands = actions.filter((r) => r.kind === "diagnostic_command");
  const unhealthy = executions.filter((te) => (evidenceByExec.get(te.id)?.health ?? "failed") !== "healthy").length;
  // A citation opens the collapsed diagnostics so the link lands on its row.
  const openDiagnostics = () => {
    const d = document.getElementById(diagnosticsID);
    if (d instanceof HTMLDetailsElement) d.open = true;
  };

  return (
    <>
      <section className="card" aria-label="Selected investigation">
        {notice}
        <div className="flex flex-wrap items-start justify-between gap-3">
          <p className={`text-xs ${muted}`}>
            {inv.status !== "completed" && <>Status: <strong className="text-zinc-900 dark:text-zinc-100">{inv.status}</strong>{" · "}</>}
            Started <time dateTime={inv.started_at} title={inv.started_at}>{timeAgo(inv.started_at)}</time>
            {inv.completed_at && <>{" · "}finished <time dateTime={inv.completed_at} title={inv.completed_at}>{timeAgo(inv.completed_at)}</time></>}
            {report && <>{" · "}by <span className="font-mono">{report.provider}</span></>}
          </p>
          {toolbar && <div className="flex flex-wrap gap-2">{toolbar}</div>}
        </div>
        {inv.error && <p role="alert" className="mt-3 whitespace-pre-wrap break-words rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300">{inv.error}</p>}
        {inv.status === "queued" && <p className={`mt-3 text-sm ${muted}`}>This investigation is queued and waiting for a worker.</p>}
        {inv.status === "running" && <p className={`mt-3 text-sm ${muted}`}>This investigation is running. Its saved diagnostic results appear below.</p>}

        {a && report && (
          <div className="mt-4" aria-label={v2 ? "Assessment" : "Legacy assessment"} role="group">
            <div className="flex flex-wrap items-center gap-2">
              {v2 && a.verdict && <span className={`rounded-full px-2.5 py-0.5 text-xs font-semibold ${verdictStyle[a.verdict] ?? verdictStyle.inconclusive}`}>{verdictLabel[a.verdict] ?? a.verdict}</span>}
              <span className={`rounded-md border px-2 py-0.5 text-[11px] font-bold uppercase tracking-wide ${severityStyle[a.severity]}`}>{a.severity}</span>
              <Hint topic="the verdict and severity">
                <strong>Observed fault</strong>: a cited observation shows the problem. <strong>Suspected fault</strong>: the evidence points to it without proving it.{" "}
                <strong>No fault observed</strong>: no known fault in these snapshots. <strong>Inconclusive</strong>: data is missing or no known pattern matched.{" "}
                Severity (low to critical) is the analyzer's estimate of impact, not a measurement.
              </Hint>
            </div>
            {aiNotice(report.provider) && <p className="mt-3 rounded-lg border border-amber-500/40 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:bg-amber-500/10 dark:text-amber-200">{aiNotice(report.provider)}</p>}
            <h3 className="mt-3 text-xl font-semibold text-balance break-words">{a.root_cause}</h3>
            <p className="mt-2 max-w-[70ch] text-sm text-zinc-700 dark:text-zinc-300">{a.summary}</p>
            {v2 ? (
              <p className="mt-3 flex flex-wrap items-center gap-1.5 text-sm">
                <span>Evidence support: <strong>{a.support}</strong></span>
                <Hint topic="evidence support">
                  How much of the collected evidence backs the conclusion: strong, limited or insufficient. It describes the evidence; it is not a probability that the conclusion is right.
                </Hint>
              </p>
            ) : <p className={`mt-3 text-xs ${muted}`}>Legacy report: tool-level citations and an uncalibrated score. The numeric score is not displayed.</p>}

            {(a.evidence ?? []).length > 0 && (
              <>
                <h4 className="section-title mt-5">Cited evidence</h4>
                <ul className="flex flex-col gap-2 text-sm">
                  {(a.evidence ?? []).map((e, i) => (
                    <li key={i} className="break-words">
                      {v2 && e.evidence_id && citedIDs.has(e.evidence_id)
                        ? <a className="mr-2 font-mono text-xs text-indigo-700 underline underline-offset-2 dark:text-indigo-300" href={`#${anchor(e.evidence_id)}`} title={e.evidence_id} onClick={openDiagnostics}>{e.source}</a>
                        : <span className={`mr-2 font-mono text-xs ${muted}`}>{e.source}</span>}
                      {e.description}
                    </li>
                  ))}
                </ul>
              </>
            )}
            {v2 && (a.missing_data ?? []).length > 0 && (
              <>
                <Titled className="mt-5 mb-2" title="Missing data" hint="Diagnostics that failed or returned nothing (no permission, no logs, no metrics). While core data is missing, the workload is not called healthy." />
                <ul className="list-disc pl-5 text-sm">{a.missing_data?.map((item, i) => <li key={i} className="break-words">{item}</li>)}</ul>
              </>
            )}
          </div>
        )}
      </section>

      {a && report && (recommendations.length > 0 || commands.length > 0) && (
        <section className="card" aria-label="Next steps">
          {recommendations.length > 0 && <>
            <Titled className="mb-2" title="Remediation" hint="Changes you would make yourself, in this order; the troubleshooter never changes the cluster." />
            <ol className="flex flex-col gap-2 text-sm">
              {recommendations.map((r) => (
                <li key={r.id} className="flex items-start gap-2">
                  <span className={`mt-0.5 font-mono text-xs ${muted}`}>{r.position}.</span>
                  <span className="flex-1">{r.description}</span>
                </li>
              ))}
            </ol>
          </>}
          {commands.length > 0 && <>
            <Titled className={`mb-2 ${recommendations.length > 0 ? "mt-5" : ""}`} title="Read-only commands" hint="kubectl commands that only read (get, describe, logs, top, events, rollout status or history), checked to contain no write verb and no Secret access. Copy them to look further." />
            <ul className="flex flex-col gap-1.5">
              {commands.map((c) => <CommandLine key={c.id} command={c.command ?? ""} />)}
            </ul>
          </>}
        </section>
      )}

      {a && report && (
        <details className="card disclosure">
          <summary>Hypotheses and sampling limits</summary>
          <div className="mt-4">
            <h4 className="section-title">Possible causes (hypotheses)</h4>
            <ul className="flex list-disc flex-col gap-1.5 pl-5 text-sm">
              {(a.possible_causes ?? []).map((t) => <li key={t}>{t}</li>)}
            </ul>
            {v2 && <>
              <h4 className="section-title mt-5">Scope and sampling limits</h4>
              <ul className="list-disc pl-5 text-sm">{a.limitations?.map((item, i) => <li key={i} className="break-words">{item}</li>)}</ul>
            </>}
            {(a.affected_resources ?? []).length > 0 && <>
              <h4 className="section-title mt-5">Affected resources</h4>
              <div className="flex flex-wrap gap-1.5">{a.affected_resources.map((r) => <span key={r} className={chip}>{r}</span>)}</div>
            </>}
          </div>
        </details>
      )}

      <details id={diagnosticsID} className="card disclosure" open={!report}>
        <summary>
          Diagnostics
          <span className={`text-xs font-normal ${muted}`}>
            {executions.length} {executions.length === 1 ? "check" : "checks"}{unhealthy > 0 && `, ${unhealthy} not healthy`}
          </span>
        </summary>
        <div className="mt-4 overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className={`text-xs ${muted}`}>
              <tr className="border-b border-zinc-200 dark:border-zinc-800">
                <th className="py-2 pr-4 font-medium">Tool</th>
                <th className="py-2 pr-4 font-medium">Result</th>
                <th className="py-2 font-medium">Observation</th>
              </tr>
            </thead>
            <tbody>
              {executions.map((te) => {
                const ev = evidenceByExec.get(te.id);
                const health = ev?.health ?? "failed";
                // Chips only for signals the observation text does not already name.
                const signals = (ev?.signals ?? []).filter((s) => !ev?.summary.includes(s));
                return (
                  <tr key={te.id} id={ev && anchor(ev.id)} className="scroll-mt-20 border-b border-zinc-100 align-top last:border-0 dark:border-zinc-800/60">
                    <td className="py-2.5 pr-4">
                      <div className="font-mono text-xs">{te.tool_name}</div>
                      {ev?.subject && <div className={`mt-0.5 font-mono text-[11px] ${muted}`}>{ev.subject}</div>}
                    </td>
                    <td className={`py-2.5 pr-4 text-xs font-semibold whitespace-nowrap ${healthStyle[health]}`}>
                      <span className="mr-1.5 inline-block size-2 rounded-full bg-current" />
                      {health}
                    </td>
                    <td className="py-2.5 break-words">
                      {ev?.summary ?? te.error}
                      {signals.length > 0 && (
                        <div className="mt-1.5 flex flex-wrap gap-1">
                          {signals.map((s) => <span key={s} className={chip}>{s}</span>)}
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </details>
    </>
  );
}

function CommandLine({ command }: { command: string }) {
  const [copied, setCopied] = useState<"" | "copied" | "copy failed: select the command">("");
  return (
    <li className="flex items-center gap-2 rounded-lg bg-zinc-100 px-3 py-1.5 dark:bg-zinc-800">
      <code className="flex-1 truncate font-mono text-xs">{command}</code>
      <button
        type="button"
        className="shrink-0 rounded px-1.5 text-xs text-zinc-700 underline-offset-2 hover:underline dark:text-zinc-300"
        onClick={() =>
          // Clipboard access can be denied or missing (plain HTTP); say so instead of failing silently.
          Promise.resolve().then(() => navigator.clipboard.writeText(command)).then(
            () => { setCopied("copied"); setTimeout(() => setCopied(""), 1500); },
            () => setCopied("copy failed: select the command"))
        }
      >
        {copied || "copy"}
      </button>
    </li>
  );
}
