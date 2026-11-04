import type { InvestigationPage, InvestigationRecord } from "../api";
import { timeAgo } from "../format";

interface Props {
  page: InvestigationPage | null;
  selectedID?: string;
  latestReport: InvestigationRecord | null;
  busy: boolean;
  error: string;
  onSelect: (id: string) => void;
  onPage: (offset: number) => void;
  onRefresh: () => void;
}

export default function InvestigationHistory({ page, selectedID, latestReport, busy, error, onSelect, onPage, onRefresh }: Props) {
  const successful = latestReport?.investigation;
  return (
    // Collapsed by default: the summary line carries counts and the latest successful report.
    <section className="card" aria-label="Investigation history">
      {error && <p role="alert" className="mb-2 text-sm text-red-600 dark:text-red-400">{error}</p>}
      <details className="disclosure">
        <summary>
          Investigation history
          <span className="text-xs font-normal text-zinc-600 dark:text-zinc-400">
            {page && `${page.investigations.length}${page.has_more ? "+" : ""} ${page.investigations.length === 1 && !page.has_more ? "attempt" : "attempts"}`}
            {successful && <>{" · "}Latest successful report: <time dateTime={latestReport?.report?.created_at} title={latestReport?.report?.created_at}>{latestReport?.report?.created_at && timeAgo(latestReport.report.created_at)}</time></>}
          </span>
        </summary>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button type="button" className="btn-secondary" disabled={busy} onClick={onRefresh}>Refresh history</button>
        {successful && <button type="button" className="btn-secondary" disabled={busy} onClick={() => onSelect(successful.id)}>View successful report</button>}
      </div>
      {page && page.investigations.length === 0 && <p className="mt-3 text-sm text-zinc-600 dark:text-zinc-400">No investigations on this page.</p>}
      <ul className="mt-3 flex flex-col gap-2">
        {page?.investigations.map((attempt, index) => (
          <li key={attempt.id}>
            <button type="button" className="w-full rounded-lg border border-zinc-200 p-3 text-left text-sm dark:border-zinc-700 aria-pressed:border-indigo-500 aria-pressed:bg-indigo-50 dark:aria-pressed:bg-indigo-950"
              aria-pressed={selectedID === attempt.id} disabled={busy} onClick={() => onSelect(attempt.id)}>
              <strong>{attempt.status}</strong>{page.offset === 0 && index === 0 && <span> · Latest attempt</span>}{" · "}
              <time dateTime={attempt.started_at} title={attempt.started_at}>{timeAgo(attempt.started_at)}</time>
              {attempt.scenario && <span> · {attempt.scenario}</span>}
              <span className="mt-1 block font-mono text-xs text-zinc-600 dark:text-zinc-400">{attempt.id}</span>
            </button>
          </li>
        ))}
      </ul>
      {page && (
        <div className="mt-3 flex items-center gap-3 text-sm">
          <button type="button" className="btn-secondary" disabled={busy || page.offset === 0} onClick={() => onPage(Math.max(0, page.offset - page.limit))}>Newer attempts</button>
          <span>Page {Math.floor(page.offset / page.limit) + 1}</span>
          <button type="button" className="btn-secondary" disabled={busy || !page.has_more} onClick={() => onPage(page.offset + page.limit)}>Older attempts</button>
        </div>
      )}
      </details>
    </section>
  );
}
