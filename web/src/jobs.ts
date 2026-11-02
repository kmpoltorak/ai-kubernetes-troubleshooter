import { api, ApiError, type JobRecord } from "./api";

export type JobSession = { key: string; scenario: string } | { jobID: string };
const storageKey = (id: string) => `investigation-job:${id}`;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function readSession(id: string): JobSession | null {
  const value = sessionStorage.getItem(storageKey(id));
  if (value === null) return null;
  const entry = JSON.parse(value);
  if (entry && typeof entry === "object") {
    if (typeof entry.jobID === "string" && uuid.test(entry.jobID)) return { jobID: entry.jobID };
    if (typeof entry.key === "string" && uuid.test(entry.key) && typeof entry.scenario === "string") return { key: entry.key, scenario: entry.scenario };
  }
  throw new Error("Saved investigation could not be read. Restore this tab's session data before submitting again.");
}

export function saveSession(id: string, value: JobSession | null) {
  if (value) sessionStorage.setItem(storageKey(id), JSON.stringify(value));
  else sessionStorage.removeItem(storageKey(id));
}

// getRandomValues also works on plain HTTP minikube; randomUUID needs HTTPS.
function newKey() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export async function submitInvestigation(id: string, scenario: string, signal: AbortSignal): Promise<JobRecord> {
  const saved = readSession(id);
  const pending = saved && "key" in saved ? saved : { key: newKey(), scenario };
  // Keep the key and options before sending; an uncertain response must replay.
  saveSession(id, pending);
  try {
    const record = await api.submitJob(id, pending.scenario, pending.key, signal);
    saveSession(id, { jobID: record.job.id });
    return record;
  } catch (e) {
    if (e instanceof ApiError && [400, 401, 403, 404, 409, 413, 429].includes(e.status)) saveSession(id, null);
    throw e;
  }
}

export const activeJob = (record: JobRecord) => record.job.status === "queued" || record.job.status === "running";

function waitForPoll(signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const stop = () => { clearTimeout(timer); signal.removeEventListener("abort", stop); resolve(); };
    const timer = setTimeout(stop, 2000);
    signal.addEventListener("abort", stop, { once: true });
    if (signal.aborted) stop();
  });
}

// Only reads are repeated. Leaving the view cancels local observation, not work.
export async function followJob(id: string, jobID: string, signal: AbortSignal, update: (r: JobRecord) => void, error: (e: Error) => void) {
  while (!signal.aborted) {
    try {
      const record = await api.job(id, jobID, signal);
      if (signal.aborted) return;
      update(record);
      if (!activeJob(record)) return;
    } catch (e) {
      if (signal.aborted) return;
      error(e as Error);
      if (e instanceof ApiError && e.status >= 400 && e.status < 500 && e.status !== 429 && e.status !== 408) return;
    }
    await waitForPoll(signal);
  }
}
