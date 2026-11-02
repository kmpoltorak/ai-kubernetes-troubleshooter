// Typed client for the REST API. Types mirror internal/domain.

export type Health = "healthy" | "degraded" | "down";
export type Severity = "low" | "medium" | "high" | "critical";
export type ResourceType = "deployment" | "service" | "pod";

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
  summary: string;
  root_cause: string;
  confidence: number;
  severity: Severity;
  affected_resources: string[];
  evidence: { source: string; description: string }[];
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

async function request<T>(method: string, path: string, body?: unknown, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  if (body !== undefined) headers.set("Content-Type", "application/json");
  const timeout = AbortSignal.timeout(15_000);
  const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout;
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
  if (!res.ok) throw new ApiError(data.error?.message ?? `HTTP ${res.status}`, res.status);
  return data as T;
}

export const api = {
  listIncidents: () => request<{ incidents: Incident[] }>("GET", "/api/v1/incidents?limit=100").then((r) => r.incidents),
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

// Mirrors internal/simulation.Scenarios.
export const SCENARIOS = [
  "healthy",
  "crash_loop_backoff",
  "image_pull_error",
  "oom_killed",
  "readiness_probe_failure",
  "liveness_probe_failure",
  "missing_configmap",
  "service_selector_mismatch",
  "dns_failure",
  "resource_pressure",
  "network_policy_block",
] as const;
