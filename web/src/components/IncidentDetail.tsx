import { useEffect, useRef, useState } from "react";
import { api, ApiError, type BuildInfo, type Incident, type Investigation, type InvestigationPage, type InvestigationRecord, type JobRecord } from "../api";
import { download, reportJSON, reportMarkdown } from "../export";
import { activeJob, followJob, readSession, saveSession, submitInvestigation, type JobSession } from "../jobs";
import { target, timeAgo } from "../format";
import Report from "./Report";
import { Field } from "./Hint";
import InvestigationHistory from "./InvestigationHistory";
import JobStatus from "./JobStatus";

interface Props {
  id: string;
  initialScenario: string;
  scenarios: string[];
  build: BuildInfo | null;
  onChanged: () => void;
  onDelete: () => void;
}

export default function IncidentDetail({ id, initialScenario, scenarios, build, onChanged, onDelete }: Props) {
  const [incident, setIncident] = useState<Incident | null>(null);
  const [record, setRecord] = useState<InvestigationRecord | null>(null);
  const [scenario, setScenario] = useState(initialScenario);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [session, setSession] = useState<JobSession | null>(null);
  const sessionRef = useRef<JobSession | null>(null);
  const [sessionReady, setSessionReady] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [job, setJob] = useState<JobRecord | null>(null);
  const [watchError, setWatchError] = useState("");
  const [watchVersion, setWatchVersion] = useState(0);
  const selectedJobID = useRef<string | undefined>(undefined);
  const submitController = useRef<AbortController | null>(null);
  const jobID = session && "jobID" in session ? session.jobID : undefined;
  const pending = session && "key" in session ? session : null;
  const running = !!jobID && (!job || job.job.id !== jobID || activeJob(job));

  function remember(value: JobSession | null, persist = true) {
    if (persist) saveSession(id, value);
    if (value && "jobID" in value && !(sessionRef.current && "jobID" in sessionRef.current && sessionRef.current.jobID === value.jobID)) setJob(null);
    sessionRef.current = value;
    setSession(value);
  }

  const [page, setPage] = useState<InvestigationPage | null>(null);
  const [latestReport, setLatestReport] = useState<InvestigationRecord | null>(null);
  const [newest, setNewest] = useState<Investigation | null>(null);
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
    selectedJobID.current = undefined;
    try {
      const result = await api.investigation(id, attemptID);
      if (version === recordVersion.current) {
        selectedJobID.current = ["queued", "running"].includes(result.investigation.status) ? result.investigation.job_id : undefined;
        setRecord(result);
        const attempt = result.investigation;
        if (!storageError && attempt.job_id && (attempt.status === "queued" || attempt.status === "running") && !(sessionRef.current && "key" in sessionRef.current)) {
          try { remember({ jobID: attempt.job_id }); } catch (e) { setStorageError((e as Error).message); }
        }
      }
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
      if (offset === 0) setNewest(attempts.investigations[0] ?? null);
      setLatestReport(lastReport);
      if (selectNewest) {
        // ponytail: discover other sessions' active work on the loaded page; older pages remain selectable.
        const current = attempts.investigations.find((attempt) => attempt.job_id && ["queued", "running"].includes(attempt.status)) ?? attempts.investigations[0];
        if (current) await selectAttempt(current.id);
        else setRecord(null);
      }
    } catch (e) {
      if (version === historyVersion.current) setHistoryError((e as Error).message);
    } finally {
      if (version === historyVersion.current) setLoadingHistory(false);
    }
  }

  useEffect(() => {
    let saved: JobSession | null = null;
    let restoreFailed = false;
    try {
      saved = readSession(id);
      remember(saved, false);
      if (saved && "jobID" in saved) selectedJobID.current = saved.jobID;
    } catch (e) { restoreFailed = true; setStorageError((e as Error).message); }
    setSessionReady(true);
    loadHistory(0, !restoreFailed && !(saved && "jobID" in saved));
    return () => { ++historyVersion.current; ++recordVersion.current; submitController.current?.abort(); };
  }, [id]);

  useEffect(() => {
    if (!jobID) return;
    const controller = new AbortController();
    let previousAttempt = "";
    setWatchError("");
    followJob(id, jobID, controller.signal, (result) => {
      setJob(result);
      setWatchError("");
      if (selectedJobID.current === jobID) setRecord(result.result);
      if (!previousAttempt || previousAttempt !== result.result.investigation.id || !activeJob(result)) {
        loadHistory();
        if (!activeJob(result)) onChanged();
      }
      previousAttempt = result.result.investigation.id;
    }, (e) => setWatchError(e.message));
    return () => controller.abort();
  }, [id, jobID, watchVersion]);

  async function investigate() {
    if (submitting || running || storageError) return;
    const controller = new AbortController();
    submitController.current = controller;
    setSubmitting(true);
    setError("");
    try {
      const accepted = await submitInvestigation(id, scenario, controller.signal);
      if (controller.signal.aborted) return;
      remember({ jobID: accepted.job.id }, false);
      selectedJobID.current = accepted.job.id;
      ++recordVersion.current;
      setJob(accepted);
      setRecord(accepted.result);
    } catch (e) {
      if (!controller.signal.aborted) setError((e as Error).message);
    } finally {
      if (!controller.signal.aborted) {
        try { remember(readSession(id), false); } catch (e) { setStorageError((e as Error).message); }
        setSubmitting(false);
      }
    }
  }

  function exportAs(inc: Incident, rec: InvestigationRecord, ext: "md" | "json") {
    const meta = { exported_at: new Date().toISOString(), build };
    download(`incident-${inc.id.slice(0, 8)}-attempt-${rec.investigation.id.slice(0, 8)}.${ext}`,
      ext === "md" ? "text/markdown" : "application/json", (ext === "md" ? reportMarkdown : reportJSON)(inc, rec, meta));
  }

  if (!incident) {
    return (error || historyError) ? <p className="card text-sm text-red-600">{error || historyError}</p> : <div className="card h-40 animate-pulse" />;
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="card">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h2 className="text-lg font-semibold break-words">{incident.title}</h2>
            <p className="mt-1 truncate font-mono text-xs text-zinc-600 dark:text-zinc-400" title={`Incident ${incident.id}`}>
              {target(incident)}
            </p>
          </div>
          <span
            className={`shrink-0 rounded-full border px-2.5 py-0.5 text-xs font-semibold ${
              incident.status === "analyzed"
                ? "border-emerald-500/40 text-emerald-700 dark:text-emerald-400"
                : "border-zinc-300 text-zinc-600 dark:text-zinc-400 dark:border-zinc-700"
            }`}
          >
            {incident.status}
          </span>
        </div>
        {incident.description && <p className="mt-3 whitespace-pre-wrap break-words text-sm text-zinc-700 dark:text-zinc-300">{incident.description}</p>}

        <div className="mt-5 flex flex-wrap items-end gap-3">
          {scenarios.length > 0 && (
            <div className="w-60">
              <Field label="Scenario" hint="Simulation only: the fault the simulated cluster shows for this run. Default uses the server's SIMULATION_SCENARIO.">
                {(fieldID) => <select id={fieldID} className="field" value={pending?.scenario ?? scenario} disabled={submitting || running || !!pending} onChange={(e) => setScenario(e.target.value)}>
                  <option value="">Default</option>
                  {scenarios.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>}
              </Field>
            </div>
          )}
          <button type="button" className="btn-primary" onClick={investigate} disabled={!sessionReady || !!storageError || submitting || running || loadingHistory || loadingRecord}>
            {(running || submitting) && <span className="size-4 animate-spin rounded-full border-2 border-white/40 border-t-white" />}
            {submitting ? "Submitting…" : running ? "Investigation active" : pending ? "Retry submission" : "Run investigation"}
          </button>
          <button type="button" className="btn-danger ml-auto" onClick={onDelete} disabled={submitting || running}
            title={running ? "An incident with an active investigation cannot be deleted" : undefined}>
            Delete incident
          </button>
        </div>
        {running && !job && <p role="status" className="mt-3 text-sm text-zinc-600 dark:text-zinc-400">Resuming investigation status…</p>}
        {pending && <p className="mt-3 text-sm text-zinc-600 dark:text-zinc-400">Submission is unconfirmed. Retry uses the same request and does not start a duplicate investigation.</p>}
        {(error || storageError) && <p role="alert" className="mt-3 text-sm text-red-600 dark:text-red-400">{storageError || error}</p>}
        {(job || watchError) && <JobStatus record={job} error={watchError} onRetry={() => setWatchVersion((v) => v + 1)} onView={() => {
          if (job) { ++recordVersion.current; selectedJobID.current = job.job.id; setRecord(job.result); setLoadingRecord(false); }
        }} />}
      </div>

      <InvestigationHistory page={page} selectedID={record?.investigation.id} latestReport={latestReport}
        busy={submitting || loadingHistory || loadingRecord} error={historyError}
        onSelect={selectAttempt} onPage={(offset) => loadHistory(offset)} onRefresh={() => loadHistory(0, true)} />
      {loadingRecord && <p role="status" className="text-sm text-zinc-600 dark:text-zinc-400">Loading investigation…</p>}
      {record && <Report record={record}
        notice={newest && record.investigation.id !== newest.id && <p role="note" className="mb-4 rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-300">
          Older attempt from <time dateTime={record.investigation.started_at} title={record.investigation.started_at}>{timeAgo(record.investigation.started_at)}</time>.
          The latest attempt is {newest.status}, started <time dateTime={newest.started_at} title={newest.started_at}>{timeAgo(newest.started_at)}</time>.
        </p>}
        toolbar={<>
          <button type="button" className="btn-secondary" onClick={() => exportAs(incident, record, "md")}><DownloadIcon />Export Markdown</button>
          <button type="button" className="btn-secondary" onClick={() => exportAs(incident, record, "json")}><DownloadIcon />Export JSON</button>
        </>} />}
    </div>
  );
}

function DownloadIcon() {
  return (
    <svg aria-hidden="true" viewBox="0 0 16 16" className="size-4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M8 2.5v7.5M4.75 7 8 10.25 11.25 7M3 12.5h10" />
    </svg>
  );
}
