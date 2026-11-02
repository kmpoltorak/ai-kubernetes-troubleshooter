import { useEffect, useRef, useState } from "react";
import { api, ApiError, SCENARIOS, type Incident, type InvestigationPage, type InvestigationRecord } from "../api";
import { target } from "../format";
import Report from "./Report";
import InvestigationHistory from "./InvestigationHistory";

interface Props {
  id: string;
  initialScenario: string;
  onChanged: () => void;
}

export default function IncidentDetail({ id, initialScenario, onChanged }: Props) {
  const [incident, setIncident] = useState<Incident | null>(null);
  const [record, setRecord] = useState<InvestigationRecord | null>(null);
  const [scenario, setScenario] = useState(initialScenario);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);

  const [page, setPage] = useState<InvestigationPage | null>(null);
  const [latestReport, setLatestReport] = useState<InvestigationRecord | null>(null);
  const [historyError, setHistoryError] = useState("");
  const [loadingHistory, setLoadingHistory] = useState(false);
  const [loadingRecord, setLoadingRecord] = useState(false);
  const historyVersion = useRef(0);
  const recordVersion = useRef(0);

  async function selectAttempt(attemptID: string) {
    const version = ++recordVersion.current;
    setLoadingRecord(true);
    setHistoryError("");
    setRecord(null);
    try {
      const result = await api.investigation(id, attemptID);
      if (version === recordVersion.current) setRecord(result);
    } catch (e) {
      if (version === recordVersion.current) setHistoryError((e as Error).message);
    } finally {
      if (version === recordVersion.current) setLoadingRecord(false);
    }
  }

  async function loadHistory(offset = 0, selectNewest = false) {
    const version = ++historyVersion.current;
    setLoadingHistory(true);
    setHistoryError("");
    try {
      const [inc, attempts, lastReport] = await Promise.all([
        api.getIncident(id), api.investigations(id, offset),
        api.report(id).catch((e) => {
          if (e instanceof ApiError && e.status === 404) return null;
          throw e;
        }),
      ]);
      if (version !== historyVersion.current) return;
      setIncident(inc);
      setPage(attempts);
      setLatestReport(lastReport);
      if (selectNewest) {
        if (attempts.investigations[0]) await selectAttempt(attempts.investigations[0].id);
        else setRecord(null);
      }
    } catch (e) {
      if (version === historyVersion.current) setHistoryError((e as Error).message);
    } finally {
      if (version === historyVersion.current) setLoadingHistory(false);
    }
  }

  useEffect(() => {
    loadHistory(0, true);
    return () => { ++historyVersion.current; ++recordVersion.current; };
  }, [id]);

  async function investigate() {
    setRunning(true);
    setError("");
    try {
      setRecord(await api.investigate(id, scenario));
      setIncident((inc) => inc && { ...inc, status: "analyzed" });
      onChanged();
    } catch (e) {
      setError((e as Error).message);
      // A failed analysis still returns the collected evidence.
      if (e instanceof ApiError && e.body.result) setRecord(e.body.result);
    } finally {
      await loadHistory();
      setRunning(false);
    }
  }

  if (!incident) {
    return (error || historyError) ? <p className="card text-sm text-red-600">{error || historyError}</p> : <div className="card h-40 animate-pulse" />;
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="card">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h2 className="text-lg font-semibold">{incident.title}</h2>
            <p className="mt-1 truncate font-mono text-xs text-zinc-500">
              {target(incident)} · {incident.id}
            </p>
          </div>
          <span
            className={`shrink-0 rounded-full border px-2.5 py-0.5 text-xs font-semibold ${
              incident.status === "analyzed"
                ? "border-emerald-500/40 text-emerald-600 dark:text-emerald-400"
                : "border-zinc-300 text-zinc-500 dark:border-zinc-700"
            }`}
          >
            {incident.status}
          </span>
        </div>
        {incident.description && <p className="mt-3 whitespace-pre-wrap text-sm text-zinc-700 dark:text-zinc-300">{incident.description}</p>}

        <div className="mt-5 flex flex-wrap items-end gap-3">
          <label className="label w-60">
            Scenario
            <select className="field" value={scenario} onChange={(e) => setScenario(e.target.value)}>
              <option value="">Default</option>
              {SCENARIOS.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>
          <button type="button" className="btn-primary" onClick={investigate} disabled={running || loadingHistory || loadingRecord}>
            {running && <span className="size-4 animate-spin rounded-full border-2 border-white/40 border-t-white" />}
            {running ? "Investigating…" : "Run investigation"}
          </button>
          <span className="pb-2 text-xs text-zinc-500">Scenarios apply in simulation mode only.</span>
        </div>
        {running && (
          <p className="mt-3 text-sm text-zinc-500">Running diagnostics and analysis. With a local LLM this can take a minute.</p>
        )}
        {error && <p className="mt-3 text-sm text-red-600 dark:text-red-400">{error}</p>}
      </div>

      <InvestigationHistory page={page} selectedID={record?.investigation.id} latestReport={latestReport}
        busy={running || loadingHistory || loadingRecord} error={historyError}
        onSelect={selectAttempt} onPage={(offset) => loadHistory(offset)} onRefresh={() => loadHistory(0, true)} />
      {loadingRecord && <p role="status" className="text-sm text-zinc-500">Loading investigation…</p>}
      {record && <Report record={record} />}
    </div>
  );
}
