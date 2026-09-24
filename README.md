# AI Kubernetes Troubleshooter

[![CI](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml/badge.svg)](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml)

A Go service that investigates Kubernetes workload incidents. It runs a fixed,
read-only diagnostic plan against the cluster, turns the results into
structured evidence, and asks an LLM for a root-cause report. The report is
validated before it is stored.

The application owns the workflow. The LLM only reads evidence: it has no
tools, no shell and no Kubernetes client, and it cannot trigger an API call.

## Overview

Triage of a broken workload usually means running the same dozen `kubectl`
commands, reading noisy events and logs, and correlating them by hand. This
service automates the collection step and hands the correlation step to an
LLM:

1. You file an incident: *"payment-service pods keep restarting"*.
2. The service inspects the namespace, Deployment, pods, events, current and
   previous logs, Services and EndpointSlices, referenced ConfigMaps and
   Secrets, NetworkPolicies and resource usage.
3. Each result becomes evidence with a health verdict, a summary and stable
   **signals** such as `OOMKilled`, `SelectorMismatch` or `DNSResolutionError`.
4. An analyzer (OpenAI, Ollama or the built-in deterministic rules) returns a
   JSON report. The service checks that the report cites only evidence that
   was actually collected and suggests only read-only `kubectl` commands.
5. Everything is stored in PostgreSQL: incident, every tool execution,
   evidence, report and remediation actions.

## Features

- **10 allowlisted, read-only diagnostics** on client-go: `get_namespace`,
  `get_deployment`, `get_pods`, `get_events`, `get_pod_logs`, `get_service`,
  `get_configmap`, `get_secret_metadata`, `get_network_policies`,
  `get_resource_usage`
- **Deterministic plan**: every tool input comes from earlier results (the
  selector from the Deployment, container names from the pods, ConfigMap names
  from the pod spec), never from model output
- **Secret values are never read.** Secrets are fetched as
  `PartialObjectMetadata`, env vars are reported as names and source kinds,
  and ConfigMaps as key names and sizes
- **Redaction** of logs and event messages: passwords, tokens, JWTs, API
  keys, URL credentials and private keys
- **Bounded evidence**: tail and byte limits enforced by the API server, the
  last 30 lines plus error lines per stream, 50 events, 20 pods
- **Strict output validation**: schema, confidence range, severity enum, cited
  sources, `kind/name` resources, and a read-only command allowlist that
  rejects Secret access, shell syntax and `--as`/`--token`/`--raw`
- **Simulation mode** with 11 deterministic scenarios built on client-go fake
  clients. The real tools run unchanged against them.
- **Three analyzers**: OpenAI (strict `json_schema`), Ollama (schema
  `format`), and `rules`, an offline deterministic baseline that is also the
  evaluation reference
- **Least-privilege RBAC**, bound per namespace, with no Secrets, exec,
  attach or write verbs
- REST API, PostgreSQL with embedded migrations, Prometheus metrics, JSON logs
  with request and trace IDs, per-IP rate limiting, graceful shutdown
- Distroless non-root image, Docker Compose, Kubernetes manifests, and a CI
  pipeline with lint, race tests, Postgres integration tests, govulncheck and
  a Docker build

## Architecture

```mermaid
flowchart LR
    U[User / CI] -->|REST| API[REST API<br/>net/http]
    API --> IS[Incident service<br/>validation + target policy]
    API --> IE[Investigation engine<br/>deterministic plan]
    IE --> TR[Tool registry<br/>10-tool allowlist]
    TR -->|Get / List / GetLogs| K8S[(Kubernetes API)]
    TR -.simulation.-> FAKE[(client-go fake clients<br/>scenario fixtures)]
    TR --> EN[Evidence<br/>health · summary · signals · redacted data]
    EN --> LLM[Analyzer<br/>OpenAI · Ollama · rules]
    LLM --> V{Validator}
    V -->|valid| R[Report +<br/>remediation actions]
    V -->|invalid| F[Failed investigation<br/>evidence kept]
    IS --> DB[(PostgreSQL)]
    R --> DB
    F --> DB
```

Investigation lifecycle:

```mermaid
sequenceDiagram
    participant C as Client
    participant A as API
    participant E as Engine
    participant K as Kubernetes API
    participant L as Analyzer
    participant D as PostgreSQL
    C->>A: POST /incidents/{id}/investigate
    A->>E: Investigate (concurrency slot, target policy)
    E->>D: create investigation (running)
    loop plan: namespace → workload → pods → events → logs → services → config → policies → usage
        E->>K: read-only call (per-tool timeout)
        K-->>E: objects / logs
        E->>E: normalize + redact → evidence
    end
    E->>L: incident + evidence (JSON)
    L-->>E: analysis (JSON)
    E->>E: validate (sources, schema, safe commands)
    E->>D: executions + evidence + report + actions (one transaction)
    A-->>C: 201 record / 502 with evidence
```

## Demo

```bash
docker compose up --build -d            # app + Postgres, simulation mode, rules analyzer

curl -s -X POST localhost:8080/api/v1/incidents -d @docs/examples/oom_killed.json | tee /tmp/inc.json
ID=$(jq -r .id /tmp/inc.json)
curl -s -X POST localhost:8080/api/v1/incidents/$ID/investigate -d '{"scenario":"oom_killed"}' \
  | jq '.report.analysis | {root_cause, severity, confidence, safe_commands}'
```

```json
{
  "root_cause": "Container OOMKilled: memory exhaustion",
  "severity": "high",
  "confidence": 0.94,
  "safe_commands": [
    "kubectl top pod -n payments",
    "kubectl describe pod <pod> -n payments"
  ]
}
```

## Technology Stack

| Area | Choice |
|---|---|
| Language | Go 1.27, standard library router and `log/slog` |
| Kubernetes | `k8s.io/client-go` (typed, metadata and fake clients) |
| Storage | PostgreSQL 17, `pgx` with plain SQL and embedded migrations |
| LLM | OpenAI Chat Completions (strict JSON schema), Ollama `/api/chat`, deterministic rules |
| Observability | Prometheus `client_golang`, JSON logs with request and trace IDs |
| Runtime | Distroless static image, Docker Compose, Kubernetes manifests with kustomize |
| CI | GitHub Actions: gofmt, vet, golangci-lint, race tests, integration tests, govulncheck, Docker build |

The module has seven direct dependencies: pgx, client_golang, x/time,
k8s.io/api, apimachinery, client-go and k8s.io/utils.

## Quick Start

Requirements: Go 1.27+ and Docker.

```bash
cp .env.example .env
docker compose up --build            # http://localhost:8080
```

Or run the binary against a local Postgres:

```bash
docker compose up -d postgres
make run                             # applies migrations, then serves on :8080
```

Live mode against your current kubeconfig context:

```bash
SIMULATION_ENABLED=false KUBERNETES_CONTEXT=my-cluster \
KUBERNETES_ALLOWED_NAMESPACES=payments make run
```

## Simulation Mode

With `SIMULATION_ENABLED=true` (the default), each investigation gets its own
fake cluster built around the incident's namespace and resource name. That
means Namespace, Deployment, Pods, Service, EndpointSlice, ConfigMap, Secret
metadata, NetworkPolicies, Events, container logs (served through the fake
`pods/log` reactor) and usage metrics. The real diagnostic tools run against
it, so simulation exercises the production code path. All timestamps come
from a fixed reference time, so every run gives the same result.

| Scenario | What the cluster shows | Rules analyzer verdict |
|---|---|---|
| `healthy` | 2/2 ready, no warnings | No Kubernetes-level fault |
| `crash_loop_backoff` | CrashLoopBackOff, exit 1, `missing required configuration` in previous logs | Application startup failure |
| `image_pull_error` | ImagePullBackOff, `manifest unknown`, rollout stuck | Image cannot be pulled |
| `oom_killed` | last state OOMKilled (137), memory at 98% of limit | OOMKilled: memory exhaustion |
| `readiness_probe_failure` | running, not ready, `Readiness probe failed: 503` | Readiness probe failure |
| `liveness_probe_failure` | restarts, `Liveness probe failed`, `Killing` events | Liveness probe failure |
| `missing_configmap` | CreateContainerConfigError, ConfigMap absent | Referenced ConfigMap is missing |
| `service_selector_mismatch` | healthy pods, Service selector `app=<name>-api`, no endpoints | Service selector mismatch |
| `dns_failure` | `lookup postgres-primary...: no such host` in logs | DNS resolution failure |
| `resource_pressure` | Pending pod, `Insufficient memory` FailedScheduling | Insufficient cluster resources |
| `network_policy_block` | egress policy allowing only DNS, `i/o timeout` to the DB | NetworkPolicy blocks egress |

Pick a scenario per request with `{"scenario": "dns_failure"}`, or set the
default with `SIMULATION_SCENARIO`. Live mode rejects scenario requests.

## Kubernetes Access

- **Live mode** (`SIMULATION_ENABLED=false`) loads `KUBECONFIG` and
  `KUBERNETES_CONTEXT`. If neither is set and the process runs in a pod, it
  uses the in-cluster ServiceAccount.
- **Target policy**: an incident's `cluster` must equal
  `KUBERNETES_CLUSTER_NAME`, and its namespace must be in
  `KUBERNETES_ALLOWED_NAMESPACES` when that is set. The policy is checked at
  incident creation and again before each investigation.
- All names are validated as RFC 1123 before any API call. Tools only call
  `Get`, `List` and `GetLogs`, and there is no write path in the code.
- Every API request is counted in
  `kubernetes_api_requests_total{method,code}`. `/ready` checks the API
  server with `/version`.
- If metrics-server is missing, or a permission is not granted,
  `get_resource_usage` or `get_secret_metadata` is recorded as a failed tool
  execution and the investigation continues.

## RBAC

[`deployments/kubernetes/rbac.yaml`](deployments/kubernetes/rbac.yaml).
`troubleshooter-reader` is a ClusterRole used as a permission set and granted
**per namespace with a RoleBinding**, so the ServiceAccount can read nothing
outside the bound namespaces.

| Resource | Verbs | Why |
|---|---|---|
| `pods` | get, list | pod status, restarts, container states, limits; Service selector match |
| `pods/log` | get | current and previous container logs |
| `events` | list | BackOff, Unhealthy, FailedScheduling, FailedMount, ... |
| `services` | get, list | the target Service and Services selecting the workload |
| `configmaps` | get | presence and key names of referenced ConfigMaps |
| `apps/deployments` | get | replicas, conditions, rollout, pod template |
| `discovery.k8s.io/endpointslices` | list | ready and not-ready endpoints behind a Service |
| `networking.k8s.io/networkpolicies` | list | policies selecting the workload's pods |
| `metrics.k8s.io/pods` | list | usage compared with limits (only with metrics-server) |
| `namespaces` (ClusterRole `troubleshooter-namespace-reader`) | get | confirm the target namespace exists |

**Not granted:** cluster-admin, any write verb, `pods/exec`, `pods/attach`,
`pods/portforward`, `secrets` list or watch. Kubernetes RBAC cannot grant
metadata-only access to Secrets, so `get secrets` lives in a separate
**opt-in** manifest,
[`optional/secret-metadata-rbac.yaml`](deployments/kubernetes/optional/secret-metadata-rbac.yaml).
The app reads Secrets only as `PartialObjectMetadata`. A Go test
(`deployments/kubernetes/rbac_test.go`) fails the build if any of these rules
regresses.

Checked on minikube with
`kubectl auth can-i --as=system:serviceaccount:troubleshooter:troubleshooter`:
`get pods/log -n payments` → yes; `get secrets`, `create pods/exec`,
`delete pods`, `patch deployments` in `payments` → no; `get pods -n kube-system` → no.

## API Examples

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/incidents` | create an incident |
| `GET` | `/api/v1/incidents?limit=20&offset=0` | list incidents, newest first (max 100) |
| `GET` | `/api/v1/incidents/{id}` | get an incident |
| `POST` | `/api/v1/incidents/{id}/investigate` | run an investigation; optional `{"scenario": "..."}` |
| `GET` | `/api/v1/incidents/{id}/report` | latest completed investigation with evidence and report |
| `GET` | `/health`, `/ready`, `/metrics` | liveness, readiness (database and Kubernetes), Prometheus |

```bash
curl -s -X POST localhost:8080/api/v1/incidents -d '{
  "cluster": "local",
  "namespace": "payments",
  "resource_type": "deployment",
  "resource_name": "payment-service",
  "description": "Pods are repeatedly restarting"
}'
```

`resource_type` is `deployment`, `service` or `pod`. `title` is optional.

Errors always have the same shape:

```json
{"error": {"code": "validation_failed", "message": "target: target not allowed: namespace \"kube-system\" is not in the allowed namespaces"}, "request_id": "c5be6edcd917dbe609776fe05d4b2a9a"}
```

| Status | Code | When |
|---|---|---|
| 400 | `validation_failed`, `invalid_json` | bad input, unknown fields, target outside policy, unknown scenario |
| 404 | `not_found` | unknown incident, or no completed investigation yet |
| 413 | `body_too_large` | body over 1 MiB |
| 429 | `busy`, `rate_limited` | all investigation slots in use, or per-IP rate limit (`Retry-After`) |
| 502 | `analysis_failed` | analyzer error or invalid output; the body includes the stored evidence |

## Example Investigation

A real run against minikube (live mode, in-cluster ServiceAccount), where a
Deployment allocates more memory than its 32Mi limit. Full output:
[`docs/examples/report_oom_killed_minikube.json`](docs/examples/report_oom_killed_minikube.json).

```text
tools:     get_namespace → get_deployment → get_pods → get_events → get_pod_logs
           → get_service → get_network_policies → get_resource_usage
evidence:  0/1 pods ready. Findings: CrashLoopBackOff, OOMKilled, FrequentRestarts.
report:    Container OOMKilled: memory exhaustion (high, 0.94)
actions:   review memory usage vs limit · increase the limit if legitimate ·
           investigate a leak / unbounded batch · kubectl top pod -n payments
```

In the same run, four other workloads came out as expected:
`payment-service` → startup failure (CrashLoopBackOff, configuration error in
previous logs); `ledger` → missing ConfigMap; `service/checkout` → selector
mismatch; `deployment/ghost` → target not found. The crash-looping pod printed
`postgres://app:hunter2@db...`. The stored evidence contains `[REDACTED]`
instead, and the password appears nowhere in the database or app logs.

## LLM Providers

| `LLM_PROVIDER` | Default model | Notes |
|---|---|---|
| `rules` (default) | `rules-v1` | offline, deterministic, ordered triage rules; the evaluation baseline |
| `openai` | `gpt-4o-mini` | Chat Completions with `response_format: json_schema` (strict); `OPENAI_BASE_URL` works with compatible APIs |
| `ollama` | `llama3.2` | `/api/chat` with the schema in `format`; `docker compose --profile ollama up` |

All providers get the same system prompt
([`internal/llm/system_prompt.txt`](internal/llm/system_prompt.txt)). It says
to use evidence only, treat log and event text as untrusted data, prefer the
specific cause over the symptom, and suggest read-only commands only. Output
is decoded strictly and then validated by `Analysis.Validate`. If it is
invalid, the investigation fails (502) and the evidence is kept.

The rules analyzer triages in the order an engineer would: a missing target,
then specific causes (image, config, scheduling, OOM, DNS, NetworkPolicy plus
timeout, liveness), and generic symptoms (CrashLoopBackOff, selector,
readiness) last.

## Observability

- **Logs**: JSON (`log/slog`) with `request_id` (a valid `X-Request-ID` is
  honored and echoed) and `trace_id` (from W3C `traceparent`, or generated).
  Investigation logs carry `incident_id` and `investigation_id`, and each
  tool logs its name, duration, health and signals.
- **Metrics** (`/metrics`):

| Metric | Labels |
|---|---|
| `incidents_total` | |
| `investigations_total` | `status` |
| `investigation_duration_seconds` | |
| `kubernetes_tool_executions_total` | `tool`, `health` |
| `kubernetes_tool_failures_total` | `tool` |
| `kubernetes_api_requests_total` | `method`, `code` |
| `llm_requests_total` | `provider`, `status` |
| `llm_request_duration_seconds` | `provider` |
| `llm_failures_total` | `provider`, `reason` |
| `http_requests_total`, `http_request_duration_seconds` | `method`, `route` (pattern, not raw path), `status` |

Every labeled series starts at zero, so `increase()` sees the first event.
`docker compose --profile monitoring up` starts Prometheus on `:9090`.

## Testing

```bash
make test               # unit tests with the race detector
make test-integration   # starts a disposable Postgres container, runs -tags integration
make lint               # gofmt, go vet, golangci-lint
```

- **Diagnostics**: each tool against `fake.NewClientset` and the fake
  metadata client (found and not found, signals, health, no env, ConfigMap or
  Secret values, log bounds, redaction).
- **Simulation**: all 10 tools against all 11 scenarios, asserting the
  expected signals (and the absence of misleading ones), plus a determinism
  check.
- **Evaluation**: every scenario end to end through engine, tools and rules
  analyzer, checking root cause and severity.
- **Engine**: plans for deployment, pod and service targets, early stop on a
  missing target, invalid model output rejected with evidence kept, tool
  failures recorded, busy handling, persistence after the client disconnects.
- **LLM**: strict decoding, prompt content, OpenAI and Ollama request shape
  and error mapping via `httptest`, timeouts, rule precedence, and every
  rule's commands passing the safe-command validator.
- **API**: lifecycle, error table, limits, rate limiting, headers, request
  IDs, metrics labels.
- **Integration** (PostgreSQL): migrations down and up, store round trip,
  sample incidents through REST, engine, simulated cluster, Postgres and the
  report read back.
- **Manifests**: RBAC is read-only and least-privilege.

No real cluster is needed in CI. Live mode was checked manually on minikube
(see above).

## Docker

- Multi-stage build to `gcr.io/distroless/static-debian12:nonroot`: 53 MB,
  UID 65532, no shell. The binary is PID 1 and shuts down gracefully on
  SIGTERM.
- Compose runs the app with a read-only root filesystem, all capabilities
  dropped and `no-new-privileges`.
- Profiles: `ollama` (server plus a one-shot model pull) and `monitoring`
  (Prometheus).

## Kubernetes Deployment

```bash
kubectl apply -k deployments/kubernetes
kubectl -n troubleshooter create secret generic troubleshooter-secrets \
  --from-literal=DATABASE_URL='postgres://...' --from-literal=OPENAI_API_KEY=''
# For each additional namespace: add a RoleBinding in rbac.yaml and extend
# KUBERNETES_ALLOWED_NAMESPACES in configmap.yaml.
```

The kustomization contains the Namespace (Pod Security `restricted`),
ServiceAccount, RBAC, ConfigMap (live mode), Deployment (2 replicas,
readiness `/ready`, liveness `/health`, requests and limits, non-root,
read-only root filesystem, no privilege escalation, all capabilities dropped,
`RuntimeDefault` seccomp), Service and Ingress. `secret.example.yaml` and
`optional/` are applied manually on purpose. PostgreSQL is expected to be
provided externally.

## Security

- The LLM gets data only: no tool calling, no shell, no Kubernetes client.
  The workflow is fixed in code.
- 10-tool allowlist; inputs are validated (RFC 1123) before any API call.
- Read-only RBAC bound per namespace, plus an application-level namespace
  allowlist.
- Secret values are never transferred: metadata API only, no annotations,
  env values replaced by their source kind, ConfigMaps reported as key names.
- Redaction of credentials in logs, events and status messages before
  storage or analysis.
- The system prompt treats log and event text as untrusted data. Output
  validation stops a manipulated model from citing uncollected evidence or
  suggesting write, exec or Secret commands.
- Per-tool and per-investigation timeouts, a concurrency cap, per-IP rate
  limiting, a 1 MiB body limit, and strict JSON decoding (unknown fields
  rejected).
- Internal errors are hidden from clients, and the config log line redacts
  the API key.
- Distroless non-root container, restricted Pod Security, hardened
  `securityContext`.
- **No API authentication is built in.** Expose the service only behind an
  authenticating ingress or on an internal network (see the Roadmap).

## Project Structure

```text
cmd/api/                  entrypoint: config, wiring, migrations, graceful shutdown
internal/api/             handlers, middleware (request IDs, metrics, rate limit, headers)
internal/config/          environment configuration
internal/domain/          entities, target policy, analysis and safe-command validation
internal/incidents/       incident service
internal/investigation/   engine: plan, tool execution, analysis; evaluation test
internal/kube/            client-go connection, API metrics, metrics.k8s.io reader
internal/diagnostics/     the 10 tools, signals, redaction
internal/simulation/      scenario fixtures on client-go fake clients
internal/llm/             OpenAI, Ollama and rules analyzers, prompt, schema
internal/storage/         PostgreSQL store and migration runner
internal/observability/   logger and Prometheus metrics
migrations/               SQL migrations (embedded)
deployments/kubernetes/   manifests, RBAC, RBAC test
deployments/prometheus/   scrape config
docs/examples/            sample incidents and a real report
```

## Roadmap

- StatefulSet, DaemonSet, Job/CronJob, HPA, PVC, Ingress and node diagnostics
- Prometheus queries as evidence (error rate, latency) and a Grafana dashboard
- LLM-requested follow-up diagnostics, restricted to the existing allowlist
- Remediation execution behind explicit human approval (the `remediation_actions` table already tracks status)
- Asynchronous investigations with a job queue
- API authentication and authorization, multi-cluster, Slack and GitHub integrations
- Web frontend

## License

MIT
