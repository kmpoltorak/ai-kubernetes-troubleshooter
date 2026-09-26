import { useState } from "react";
import type { Health, InvestigationRecord, Severity } from "../api";

const severityStyle: Record<Severity, string> = {
  low: "text-emerald-600 border-emerald-500/40 dark:text-emerald-400",
  medium: "text-amber-600 border-amber-500/40 dark:text-amber-400",
  high: "text-red-600 border-red-500/40 dark:text-red-400",
  critical: "text-fuchsia-600 border-fuchsia-500/40 dark:text-fuchsia-400",
};

const healthStyle: Record<Health | "failed", string> = {
  healthy: "text-emerald-600 dark:text-emerald-400",
  degraded: "text-amber-600 dark:text-amber-400",
  down: "text-red-600 dark:text-red-400",
  failed: "text-red-600 dark:text-red-400",
};

const chip = "rounded-md border border-zinc-200 px-1.5 py-0.5 font-mono text-[11px] dark:border-zinc-700";

export default function Report({ record }: { record: InvestigationRecord }) {
  const report = record.report;
  const a = report?.analysis;
  // The API returns [] for collections, but older stored records may hold null.
  const evidenceByExec = new Map((record.evidence ?? []).map((e) => [e.tool_execution_id, e]));
  const pct = a ? Math.round(a.confidence * 100) : 0;
  const actions = report?.remediation_actions ?? [];
  const recommendations = actions.filter((r) => r.kind === "recommendation");
  const commands = actions.filter((r) => r.kind === "diagnostic_command");

  return (
    <>
      {a && report && (
        <>
          <div className="card grid gap-6 md:grid-cols-[1fr_240px]">
            <div className="min-w-0">
              <p className="text-xs font-semibold uppercase tracking-wider text-zinc-500">Root cause</p>
              <h3 className="mt-1 text-xl font-semibold">{a.root_cause}</h3>
              <p className="mt-2 text-sm text-zinc-700 dark:text-zinc-300">{a.summary}</p>
              <div className="mt-3 flex flex-wrap gap-1.5">
                {(a.affected_resources ?? []).map((r) => (
                  <span key={r} className={chip}>
                    {r}
                  </span>
                ))}
              </div>
            </div>
            <div className="flex flex-col gap-3">
              <span className={`self-start rounded-md border px-2.5 py-0.5 text-xs font-bold uppercase tracking-wide ${severityStyle[a.severity]}`}>
                {a.severity}
              </span>
              <div>
                <div className="mb-1.5 flex justify-between text-xs text-zinc-500">
                  <span>Confidence</span>
                  <strong className="text-zinc-900 dark:text-zinc-100">{pct}%</strong>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-zinc-100 dark:bg-zinc-800">
                  <div className="h-full rounded-full bg-indigo-500 transition-all duration-500" style={{ width: `${pct}%` }} />
                </div>
              </div>
              <span className="font-mono text-xs text-zinc-500">analyzed by {report.provider}</span>
            </div>
          </div>

          <div className="grid gap-5 xl:grid-cols-2">
            <div className="card">
              <h4 className="section-title">Remediation</h4>
              <ol className="flex flex-col gap-2 text-sm">
                {recommendations.map((r) => (
                  <li key={r.id} className="flex items-start gap-2">
                    <span className="mt-0.5 font-mono text-xs text-zinc-500">{r.position}.</span>
                    <span className="flex-1">{r.description}</span>
                    {r.requires_approval && (
                      <span className="shrink-0 rounded-full border border-amber-500/40 px-2 py-0.5 text-[11px] text-amber-600 dark:text-amber-400">
                        needs approval
                      </span>
                    )}
                  </li>
                ))}
              </ol>
              {commands.length > 0 && (
                <>
                  <h4 className="section-title mt-5">Read-only commands</h4>
                  <ul className="flex flex-col gap-1.5">
                    {commands.map((c) => (
                      <CommandLine key={c.id} command={c.command ?? ""} />
                    ))}
                  </ul>
                </>
              )}
            </div>
            <div className="card">
              <h4 className="section-title">Possible causes</h4>
              <ul className="flex list-disc flex-col gap-1.5 pl-5 text-sm">
                {(a.possible_causes ?? []).map((t) => (
                  <li key={t}>{t}</li>
                ))}
              </ul>
              <h4 className="section-title mt-5">Cited evidence</h4>
              <ul className="flex flex-col gap-1.5 text-sm">
                {(a.evidence ?? []).map((e, i) => (
                  <li key={i}>
                    <span className="mr-2 font-mono text-xs text-zinc-500">{e.source}</span>
                    {e.description}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </>
      )}

      <div className="card">
        <h4 className="section-title">Diagnostics</h4>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="text-xs text-zinc-500">
              <tr className="border-b border-zinc-200 dark:border-zinc-800">
                <th className="py-2 pr-4 font-medium">Tool</th>
                <th className="py-2 pr-4 font-medium">Result</th>
                <th className="py-2 pr-4 font-medium">Observation</th>
                <th className="py-2 text-right font-medium">Duration</th>
              </tr>
            </thead>
            <tbody>
              {(record.tool_executions ?? []).map((te) => {
                const ev = evidenceByExec.get(te.id);
                const health = ev?.health ?? "failed";
                const signals = ev?.signals ?? [];
                return (
                  <tr key={te.id} className="border-b border-zinc-100 align-top last:border-0 dark:border-zinc-800/60">
                    <td className="py-2.5 pr-4">
                      <div className="font-mono text-xs">{te.tool_name}</div>
                      {ev?.subject && <div className="mt-0.5 font-mono text-[11px] text-zinc-500">{ev.subject}</div>}
                    </td>
                    <td className={`py-2.5 pr-4 text-xs font-semibold whitespace-nowrap ${healthStyle[health]}`}>
                      <span className="mr-1.5 inline-block size-2 rounded-full bg-current" />
                      {health}
                    </td>
                    <td className="py-2.5 pr-4">
                      {ev?.summary ?? te.error}
                      {signals.length > 0 && (
                        <div className="mt-1.5 flex flex-wrap gap-1">
                          {signals.map((s) => (
                            <span key={s} className={chip}>
                              {s}
                            </span>
                          ))}
                        </div>
                      )}
                    </td>
                    <td className="py-2.5 text-right font-mono text-xs whitespace-nowrap">{te.duration_ms} ms</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </>
  );
}

function CommandLine({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <li className="flex items-center gap-2 rounded-lg bg-zinc-100 px-3 py-1.5 dark:bg-zinc-800">
      <code className="flex-1 truncate font-mono text-xs">{command}</code>
      <button
        type="button"
        className="shrink-0 text-xs text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100"
        onClick={() =>
          navigator.clipboard.writeText(command).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          })
        }
      >
        {copied ? "copied" : "copy"}
      </button>
    </li>
  );
}
