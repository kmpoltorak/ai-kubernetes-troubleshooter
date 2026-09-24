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
  signals: string[];
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

export interface InvestigationRecord {
  investigation: { id: string; status: string; scenario?: string; error?: string };
  tool_executions: ToolExecution[];
  evidence: Evidence[];
  report?: { provider: string; analysis: Analysis; remediation_actions: RemediationAction[] };
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
    readonly body: { result?: InvestigationRecord } = {},
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(data.error?.message ?? `HTTP ${res.status}`, res.status, data);
  return data as T;
}

export const api = {
  listIncidents: () => request<{ incidents: Incident[] }>("GET", "/api/v1/incidents?limit=100").then((r) => r.incidents),
  getIncident: (id: string) => request<Incident>("GET", `/api/v1/incidents/${id}`),
  createIncident: (body: CreateIncident) => request<Incident>("POST", "/api/v1/incidents", body),
  investigate: (id: string, scenario: string) =>
    request<InvestigationRecord>("POST", `/api/v1/incidents/${id}/investigate`, scenario ? { scenario } : {}),
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
