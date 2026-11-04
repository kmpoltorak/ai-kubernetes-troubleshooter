import type { InvestigationPage, InvestigationRecord } from "../api";

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
    <section className="card" aria-label="Investigation history">
      <div className="flex items-center justify-between gap-3">
        <h3 className="section-title">Investigation history</h3>
        <button type="button" className="btn-secondary" disabled={busy} onClick={onRefresh}>Refresh history</button>
      </div>
      {successful && (
        <p className="mt-2 text-sm">
          Latest successful report: <time dateTime={latestReport?.report?.created_at}>{latestReport?.report?.created_at}</time>{" "}
          <button type="button" className="text-indigo-600 underline dark:text-indigo-400" disabled={busy} onClick={() => onSelect(successful.id)}>View successful report</button>
        </p>
      )}
      {error && <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">{error}</p>}
      {page && page.investigations.length === 0 && <p className="mt-3 text-sm text-zinc-600 dark:text-zinc-400">No investigations on this page.</p>}
      <ul className="mt-3 flex flex-col gap-2">
        {page?.investigations.map((attempt, index) => (
          <li key={attempt.id}>
            <button type="button" className="w-full rounded-lg border border-zinc-200 p-3 text-left text-sm dark:border-zinc-700 aria-pressed:border-indigo-500 aria-pressed:bg-indigo-50 dark:aria-pressed:bg-indigo-950"
              aria-pressed={selectedID === attempt.id} disabled={busy} onClick={() => onSelect(attempt.id)}>
              <strong>{attempt.status}</strong>{page.offset === 0 && index === 0 && <span> · Latest attempt</span>}{" · "}
              <time dateTime={attempt.started_at}>{attempt.started_at}</time>
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
    </section>
  );
}
