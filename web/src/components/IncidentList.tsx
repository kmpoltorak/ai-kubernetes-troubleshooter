import type { Incident } from "../api";
import { target, timeAgo } from "../format";

interface Props {
  incidents: Incident[];
  error: string;
  selectedId?: string;
  onSelect: (id: string) => void;
  onRefresh: () => void;
  page: { offset: number; limit: number; hasMore: boolean };
  onPage: (offset: number) => void;
}

export default function IncidentList({ incidents, error, selectedId, onSelect, onRefresh, page, onPage }: Props) {
  return (
    <div className="card">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold">Incidents</h2>
        <button
          type="button"
          onClick={onRefresh}
          className="rounded-md border border-zinc-200 px-2.5 py-1 text-xs text-zinc-600 dark:text-zinc-400 hover:text-zinc-900 dark:border-zinc-700 dark:hover:text-zinc-100"
        >
          Refresh
        </button>
      </div>
      {error && <p className="text-sm text-red-600">Could not load incidents: {error}</p>}
      {!error && incidents.length === 0 && <p className="text-sm text-zinc-600 dark:text-zinc-400">{page.offset > 0 ? "No incidents on this page." : "No incidents yet."}</p>}
      <ul className="-mx-2 flex max-h-[52vh] flex-col gap-1 overflow-auto">
        {incidents.map((inc) => (
          <li key={inc.id}>
            <button
              type="button"
              onClick={() => onSelect(inc.id)}
              aria-current={inc.id === selectedId}
              className="grid w-full gap-0.5 rounded-lg border border-transparent px-3 py-2 text-left hover:bg-zinc-100 aria-[current=true]:border-indigo-500 aria-[current=true]:bg-indigo-50 dark:hover:bg-zinc-800 dark:aria-[current=true]:bg-indigo-500/10"
            >
              <span className="truncate text-sm font-medium">{inc.title}</span>
              <span className="flex justify-between gap-2 text-xs text-zinc-600 dark:text-zinc-400">
                <span className="truncate font-mono">{target(inc)}</span>
                <span className="shrink-0">
                  {inc.status} · {timeAgo(inc.created_at)}
                </span>
              </span>
            </button>
          </li>
        ))}
      </ul>
      {(page.offset > 0 || page.hasMore) && (
        <div className="mt-3 flex items-center gap-3 text-sm">
          <button type="button" className="btn-secondary" disabled={page.offset === 0} onClick={() => onPage(Math.max(0, page.offset - page.limit))}>Newer incidents</button>
          <span>Page {Math.floor(page.offset / page.limit) + 1}</span>
          <button type="button" className="btn-secondary" disabled={!page.hasMore} onClick={() => onPage(page.offset + page.limit)}>Older incidents</button>
        </div>
      )}
    </div>
  );
}
