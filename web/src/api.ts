// Typed client for the REST API. Types mirror internal/domain.

export type Health = "healthy" | "degraded" | "down";
export type Severity = "low" | "medium" | "high" | "critical";
export type ResourceType = "deployment" | "service" | "pod";

export interface BuildInfo {
  version: string;
  revision: string;
  built_at: string | null;
}

export interface Readiness {
  checks: Record<string, string>;
  build?: BuildInfo;
}

async function readiness(signal: AbortSignal): Promise<Readiness> {
  const res = await fetch("/ready", { signal: AbortSignal.any([signal, AbortSignal.timeout(15_000)]) });
  if (res.status !== 200 && res.status !== 503) throw new ApiError(`HTTP ${res.status}`, res.status);
  const body = await res.json();
  const checks = body?.checks;
  if (!checks || typeof checks !== "object" || Array.isArray(checks) ||
    !Object.values(checks).every((v) => typeof v === "string") ||
    (res.status === 503 && Object.values(checks).every((v) => v === "ok"))) {
    throw new Error("Invalid readiness response");
  }
  const b = body.build;
  const build = b && typeof b.version === "string" && typeof b.revision === "string" &&
    (b.built_at === null || (typeof b.built_at === "string" && Number.isFinite(Date.parse(b.built_at))))
    ? { version: b.version, revision: b.revision, built_at: b.built_at } : undefined;
  return { checks, build };
}

// Served behind authentication; no URLs, keys or credentials.
export interface Runtime {
  mode: "simulation" | "live";
  cluster_name: string;
  allowed_namespaces: string[];
  analyzer: { provider: string; model: string };
  scenarios: string[];
}

export interface Incident {
  id: string;
  title: string;
  description: string;
  cluster: string;
  namespace: string;
  resource_type: ResourceType;
  resource_name: string;
  status: "open" | "analyzed";
  created_at: string;
}

export interface ToolExecution {
  id: string;
  tool_name: string;
  status: "succeeded" | "failed";
  error?: string;
  duration_ms: number;
}

export interface Evidence {
  id: string;
  tool_execution_id: string;
  source: string;
  subject: string;
  health: Health;
  summary: string;
  signals: string[] | null;
}

export interface Analysis {
  schema_version?: number;
  verdict?: "observed_fault" | "suspected_fault" | "no_fault_observed" | "inconclusive";
  support?: "strong" | "limited" | "insufficient";
  missing_data?: string[];
  limitations?: string[];
  summary: string;
  root_cause: string;
  confidence?: number;
  severity: Severity;
  affected_resources: string[];
  evidence: { evidence_id?: string; source: string; description: string }[];
  possible_causes: string[];
  recommended_actions: string[];
  safe_commands: string[];
}

export interface RemediationAction {
  id: string;
  position: number;
  kind: "recommendation" | "diagnostic_command";
  description: string;
  command?: string;
  requires_approval: boolean;
  status: string;
}

export interface Investigation {
  id: string;
  incident_id: string;
  job_id?: string;
  execution_attempt?: number;
  status: "queued" | "running" | "completed" | "failed";
  scenario?: string;
  error?: string;
  started_at: string;
  completed_at?: string;
}

export interface InvestigationPage {
  investigations: Investigation[];
  offset: number;
  limit: number;
  has_more: boolean;
}

export interface InvestigationRecord {
  investigation: Investigation;
  tool_executions: ToolExecution[] | null;
  evidence: Evidence[] | null;
  report?: { created_at: string; provider: string; analysis: Analysis; remediation_actions: RemediationAction[] };
}

export interface JobRecord {
  job: {
    id: string;
    incident_id: string;
    status: Investigation["status"];
    stage: "queued" | "diagnostics" | "analyzing" | "completed" | "failed";
    attempts: number;
    deadline_at: string;
    error?: string;
  };
  result: InvestigationRecord;
}

export interface CreateIncident {
  title?: string;
  description: string;
  cluster: string;
  namespace: string;
  resource_type: ResourceType;
  resource_name: string;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => { clearTimeout(timer); reject(signal.reason); }, { once: true });
  });
}

// The server rejects rate_limited requests before handling them, so any method can be resent.
// Resend until the 15s timeout; jitter spreads requests that were refused together, since the
// UI's own burst after a reload can exceed the per-IP bucket. Other 429s (a full job queue)
// reach the caller.
function retryDelay(res: Response): number {
  const header = res.headers.get("Retry-After");
  const seconds = header === null ? 1 : Math.min(Math.max(Number(header) || 0, 0), 5);
  return seconds * 1000 + Math.random() * 500;
}

async function request<T>(method: string, path: string, body?: unknown, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  if (body !== undefined) headers.set("Content-Type", "application/json");
  const timeout = AbortSignal.timeout(15_000);
  const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout;
  for (;;) {
    const res = await fetch(path, {
      ...options,
      method,
      headers,
      signal,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const data = await res.json().catch((e) => {
      if (signal.aborted || res.ok) throw e;
      return {};
    });
    if (res.status === 429 && data.error?.code === "rate_limited") {
      await pause(retryDelay(res), signal).catch((e) => {
        // Still limited when the timeout fires: report the limit, not a generic timeout.
        throw timeout.aborted ? new ApiError(data.error.message, 429) : e;
      });
      continue;
    }
    if (!res.ok) throw new ApiError(data.error?.message ?? `HTTP ${res.status}`, res.status);
    return data as T;
  }
}

export const api = {
  readiness,
  runtime: () => request<Runtime>("GET", "/api/v1/runtime"),
  listIncidents: (limit: number, offset: number) =>
    request<{ incidents: Incident[] }>("GET", `/api/v1/incidents?limit=${limit}&offset=${offset}`).then((r) => r.incidents),
  getIncident: (id: string) => request<Incident>("GET", `/api/v1/incidents/${id}`),
  createIncident: (body: CreateIncident) => request<Incident>("POST", "/api/v1/incidents", body),
  submitJob: (id: string, scenario: string, key: string, signal: AbortSignal) =>
    request<JobRecord>("POST", `/api/v1/incidents/${id}/jobs`, scenario ? { scenario } : {}, { signal, headers: { "Idempotency-Key": key } }),
  job: (id: string, jobID: string, signal: AbortSignal) =>
    request<JobRecord>("GET", `/api/v1/incidents/${id}/jobs/${jobID}`, undefined, { signal }),
  investigations: (id: string, offset = 0) =>
    request<InvestigationPage>("GET", `/api/v1/incidents/${id}/investigations?limit=20&offset=${offset}`),
  investigation: (id: string, investigationID: string) =>
    request<InvestigationRecord>("GET", `/api/v1/incidents/${id}/investigations/${investigationID}`),
  report: (id: string) => request<InvestigationRecord>("GET", `/api/v1/incidents/${id}/report`),
};
