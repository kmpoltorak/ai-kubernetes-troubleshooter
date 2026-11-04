import type { JobRecord } from "../api";
import { activeJob } from "../jobs";

export default function JobStatus({ record, error, onRetry, onView }: { record: JobRecord | null; error: string; onRetry: () => void; onView: () => void }) {
  return (
    <section className="mt-3 text-sm" aria-label="Investigation job">
      {record && <>
        <p role="status">Job: <strong>{record.job.status}</strong> · Stage: <strong>{record.job.stage}</strong> · Attempt {record.job.attempts} of 3</p>
        <p className="mt-1 font-mono text-xs text-zinc-600 dark:text-zinc-400">{record.job.id}</p>
        {activeJob(record) && <p className="mt-1 text-zinc-600 dark:text-zinc-400">Checking every 2 seconds. Leaving this page does not stop the investigation.</p>}
        {record.job.error && <p role="alert" className="mt-1 text-red-600 dark:text-red-400">{record.job.error}</p>}
        <button type="button" className="mt-1 text-indigo-600 underline dark:text-indigo-400" onClick={onView}>View current attempt</button>
      </>}
      {error && <p role="alert" className="mt-2 text-red-600 dark:text-red-400">Status unavailable: {error}. Saved results remain below. <button type="button" className="underline" onClick={onRetry}>Retry status check</button></p>}
    </section>
  );
}
