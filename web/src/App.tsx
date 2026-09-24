import { useCallback, useEffect, useState } from "react";
import { api, type Incident } from "./api";
import CreateIncidentForm from "./components/CreateIncidentForm";
import IncidentList from "./components/IncidentList";
import IncidentDetail from "./components/IncidentDetail";

export default function App() {
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [listError, setListError] = useState("");
  const [selected, setSelectedState] = useState<{ id: string; scenario: string } | null>(() => {
    const id = new URLSearchParams(location.search).get("incident");
    return id ? { id, scenario: "" } : null;
  });

  // Keep the selection in the URL so an incident can be linked directly.
  const setSelected = (sel: { id: string; scenario: string }) => {
    setSelectedState(sel);
    history.replaceState(null, "", `?incident=${encodeURIComponent(sel.id)}`);
  };
  const [ready, setReady] = useState<Record<string, string> | null>(null);

  const refresh = useCallback(async () => {
    try {
      setIncidents(await api.listIncidents());
      setListError("");
    } catch (e) {
      setListError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    refresh();
    // /ready answers 503 with the failing checks, so read the body either way.
    const check = () =>
      fetch("/ready")
        .then((r) => r.json())
        .then((b) => setReady(b.checks ?? {}), () => setReady({ api: "unreachable" }));
    check();
    const t = setInterval(check, 30_000);
    return () => clearInterval(t);
  }, [refresh]);

  return (
    <div className="min-h-screen bg-zinc-50 text-zinc-900 dark:bg-zinc-950 dark:text-zinc-100">
      <header className="sticky top-0 z-10 border-b border-zinc-200 bg-white/80 backdrop-blur dark:border-zinc-800 dark:bg-zinc-950/80">
        <div className="mx-auto flex max-w-7xl items-center justify-between px-6 py-3">
          <div className="flex items-center gap-3">
            <div className="size-8 rounded-lg bg-gradient-to-br from-sky-500 to-indigo-500" aria-hidden />
            <div>
              <h1 className="text-sm font-semibold">Kubernetes Troubleshooter</h1>
              <p className="text-xs text-zinc-500">AI-assisted workload triage</p>
            </div>
          </div>
          <div className="flex items-center gap-4 text-sm">
            <a href="/metrics" target="_blank" rel="noopener" className="text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100">
              Metrics
            </a>
            <StatusPill ready={ready} />
          </div>
        </div>
      </header>

      <main className="mx-auto grid max-w-7xl gap-5 px-6 py-6 lg:grid-cols-[360px_1fr]">
        <aside className="flex min-w-0 flex-col gap-5">
          <CreateIncidentForm
            onCreated={(inc, scenario) => {
              setSelected({ id: inc.id, scenario });
              refresh();
            }}
          />
          <IncidentList
            incidents={incidents}
            error={listError}
            selectedId={selected?.id}
            onSelect={(id) => setSelected({ id, scenario: "" })}
            onRefresh={refresh}
          />
        </aside>
        <section className="min-w-0">
          {selected ? (
            <IncidentDetail key={selected.id} id={selected.id} initialScenario={selected.scenario} onChanged={refresh} />
          ) : (
            <EmptyState />
          )}
        </section>
      </main>
    </div>
  );
}

function StatusPill({ ready }: { ready: Record<string, string> | null }) {
  const failing = ready ? Object.entries(ready).filter(([, v]) => v !== "ok").map(([k]) => k) : [];
  const [text, cls] =
    ready === null
      ? ["checking…", "text-zinc-500"]
      : failing.length === 0
        ? [`ready · ${Object.keys(ready).join(", ")}`, "text-emerald-600 dark:text-emerald-400"]
        : [`${failing.join(", ")} unavailable`, "text-red-600 dark:text-red-400"];
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full border border-zinc-200 px-2.5 py-0.5 text-xs font-medium dark:border-zinc-700 ${cls}`}>
      <span className="size-1.5 rounded-full bg-current" />
      {text}
    </span>
  );
}

function EmptyState() {
  return (
    <div className="flex h-full min-h-80 flex-col items-center justify-center rounded-xl border border-dashed border-zinc-300 p-10 text-center dark:border-zinc-700">
      <h2 className="text-base font-semibold">Select or create an incident</h2>
      <p className="mt-2 max-w-md text-sm text-zinc-500">
        The troubleshooter reads pods, events, logs, the Deployment, Services, config references, NetworkPolicies and
        resource usage (read-only), then an analyzer correlates the evidence into a validated report with a root
        cause, confidence and safe next steps.
      </p>
    </div>
  );
}
