import { useRef, type ReactNode } from "react";
import type { Runtime } from "../api";

// The settings this installation runs with, read-only; they change in the
// environment (.env or ConfigMap), not here. The API returns no keys, URLs, kubeconfig contexts or OIDC settings.
export default function SettingsDialog({ runtime }: { runtime: Runtime }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const a = runtime.analyzer;
  const l = runtime.limits;
  const seconds = (n?: number) => `${n} s`;
  type Row = [string, ReactNode];
  const rows: Row[] = [
    ["Mode", runtime.mode],
    ...(runtime.default_scenario ? [["Default scenario", runtime.default_scenario] as Row] : []),
    ["Analyzer", `${a.provider} · ${a.model}`],
    ...(a.reasoning_effort
      ? ([["Reasoning effort", a.reasoning_effort], ["Output limit", `${a.max_output_tokens} tokens`], ["Input limit", `${a.max_input_bytes} bytes`], ["LLM timeout", seconds(a.timeout_seconds)]] as Row[])
      : [["LLM settings", "not used by the rules analyzer"] as Row]),
    ["Clusters", runtime.clusters.map((c) => `${c.name} (${c.allowed_namespaces.length ? c.allowed_namespaces.join(", ") : "all readable namespaces"})`).join("; ")],
    ["Custom resources", runtime.resource_types.filter((k) => k.includes(".")).join(", ") || "none"],
    ["Login", runtime.authentication ? "on" : "off"],
    ["Investigation timeout", seconds(l.investigation_timeout_seconds)],
    ["Job timeout", seconds(l.job_timeout_seconds)],
    ["Concurrent investigations", l.max_concurrent_investigations],
    ["Job queue capacity", l.job_queue_capacity],
    ["Rate limit", `${l.rate_limit_rps} requests/s per client`],
    ["Environment", runtime.environment],
    ["Log level", runtime.log_level],
  ];
  return (
    <>
      <button type="button" className="text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100" onClick={() => dialog.current?.showModal()}>
        Settings
      </button>
      <dialog ref={dialog} aria-labelledby="settings-title" className="w-[min(92vw,36rem)] rounded-xl border border-zinc-200 bg-white p-0 text-zinc-900 backdrop:bg-black/40 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100">
        <div className="flex items-center justify-between border-b border-zinc-200 px-5 py-3 dark:border-zinc-700">
          <h2 id="settings-title" className="text-sm font-semibold">Settings</h2>
          <button type="button" className="text-sm text-zinc-600 hover:text-zinc-900 dark:text-zinc-400 dark:hover:text-zinc-100" onClick={() => dialog.current?.close()}>
            Close
          </button>
        </div>
        <dl className="grid grid-cols-[minmax(0,11rem)_minmax(0,1fr)] gap-x-4 gap-y-2 px-5 py-4 text-sm">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-zinc-600 dark:text-zinc-400">{k}</dt>
              <dd className="break-words font-mono text-xs leading-5">{v}</dd>
            </div>
          ))}
        </dl>
        <p className="border-t border-zinc-200 px-5 py-3 text-xs text-zinc-600 dark:border-zinc-700 dark:text-zinc-400">
          Read-only. Change these in the environment (<code>.env</code> or the ConfigMap) and restart; see the configuration reference in Docs.
        </p>
      </dialog>
    </>
  );
}
