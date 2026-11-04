# AI Kubernetes Troubleshooter

[![CI](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml/badge.svg)](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml)

A Go service that investigates broken Kubernetes workloads (Deployment, Service
or Pod). It runs a fixed, read-only diagnostic plan against the cluster, turns
the results into structured evidence with signals such as `OOMKilled` or
`SelectorMismatch`, and has an LLM or a built-in rules engine write a
assessment of observed faults, hypotheses and missing data, validated before storage. It runs offline
against simulated clusters, or live with a read-only ServiceAccount.

```text
$ ID=$(curl -s -X POST localhost:8080/api/v1/incidents -d @docs/examples/oom_killed.json | jq -r .id)
$ curl -s -X POST localhost:8080/api/v1/incidents/$ID/investigate -d '{"scenario":"oom_killed"}' \
    | jq '.report.analysis | {schema_version, root_cause, verdict, support, severity, safe_commands}'
{
  "schema_version": 2,
  "root_cause": "Container OOMKilled: memory exhaustion",
  "verdict": "observed_fault",
  "support": "strong",
  "severity": "high",
  "safe_commands": [
    "kubectl top pod -n payments",
    "kubectl describe pod <pod> -n payments"
  ]
}
```

- The LLM gets data only: no tools, shell or Kubernetes client. The 10 diagnostics are a fixed allowlist calling only `Get`, `List` and `GetLogs`, and every tool input comes from earlier results, never from model output.
- Secret values are never read: Secrets are fetched as metadata only, env vars are reported as names and source kinds, ConfigMaps as key names and sizes.
- Passwords, tokens, JWTs, API keys, URL credentials and private keys are masked in incident text, diagnostic data, reports and errors before storage, API responses and LLM requests, including multi-line PEM blocks.
- New reports cite exact evidence IDs from their attempt and suggest only read-only `kubectl` commands (no Secret access, shell syntax, line breaks, `--as`, `--token` or `--raw`). Invalid output fails the investigation and keeps the evidence (`502` on the waiting endpoint).
- Failed diagnostics and sampling limits remain in the report. New reports cannot claim `no_fault_observed` when core diagnostics failed or collected observations are unhealthy; support is qualitative, never a probability.

## Install

Requires Docker with Compose. Go 1.27+ (and Node 22 for the UI) only to run
outside Docker.
Releases follow SemVer from 1.0.0. For a digest-pinned Kubernetes image and manifests, see [releases](docs/releases.md).

```bash
git clone https://github.com/kmpoltorak/ai-kubernetes-troubleshooter.git
cd ai-kubernetes-troubleshooter
cp .env.example .env
```

## Quick start

```bash
docker compose up --build                          # UI and API on http://localhost:8080
docker compose --profile monitoring up --build     # plus Prometheus :9090 and Grafana :3000
docker compose --profile ollama up --build         # plus a local Ollama with a model pull
```

Live cluster from Docker, with a kubeconfig for a read-only ServiceAccount
(the [RBAC](deployments/kubernetes/rbac.yaml) below; readable by the container's uid 65532):

```bash
KUBECONFIG_FILE=$HOME/.kube/troubleshooter.yaml KUBERNETES_CLUSTER_NAME=lab \
  KUBERNETES_ALLOWED_NAMESPACES=payments \
  docker compose -f docker-compose.yml -f docker-compose.live.yml up --build
```

The UI's sample buttons fill in an incident and pick the matching simulation
scenario. The header shows the mode, cluster name and analyzer; live mode has no
samples or scenario choice, and the form pre-fills the configured cluster. Outside Docker:

```bash
docker compose up -d postgres
make web    # optional: builds the UI into the binary
make run    # applies migrations, serves on :8080
SIMULATION_ENABLED=false KUBERNETES_CONTEXT=my-cluster KUBERNETES_ALLOWED_NAMESPACES=payments make run   # live
```

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `APP_ENV`, `HTTP_PORT`, `LOG_LEVEL` | `development`, `8080`, `info` | |
| `AUTH_ENABLED` | `false` | no login/provider needed when off; checks UI, API and metrics through OAuth2 Proxy when on |
| `AUTH_PROXY_URL` | `http://127.0.0.1:4180` | internal OAuth2 Proxy origin; Compose uses its own service address |
| `AUTH_PUBLIC_URL` | empty | required when auth is on; HTTPS origin, or HTTP loopback for local auth tests |
| `DATABASE_URL` | | PostgreSQL; Compose points it at its own container |
| `SIMULATION_ENABLED` | `true` | simulated clusters instead of a real one |
| `SIMULATION_SCENARIO` | `healthy` | default scenario |
| `KUBECONFIG`, `KUBERNETES_CONTEXT` | empty | live mode; empty uses `~/.kube/config`, or the in-cluster ServiceAccount in a pod |
| `KUBERNETES_CLUSTER_NAME` | `local` | the only value accepted in an incident's `cluster` |
| `KUBERNETES_ALLOWED_NAMESPACES` | empty (all readable) | comma-separated allowlist, checked at creation and before each investigation |
| `LLM_PROVIDER` | `rules` | `rules`, `openai`, `ollama` |
| `LLM_MODEL` | per provider | `gpt-4o-mini`, `llama3.2` |
| `LLM_MAX_INPUT_BYTES` | `262144` | entire serialized OpenAI/Ollama request body; overflow fails analysis without sending, preserving evidence |
| `LLM_TIMEOUT` | `60s` | per LLM request |
| `OPENAI_API_KEY`, `OPENAI_BASE_URL` | | any OpenAI-compatible API |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | |
| `INVESTIGATION_TIMEOUT` | `90s` | per execution attempt |
| `JOB_TIMEOUT`, `JOB_QUEUE_CAPACITY` | `15m`, `100` | total time from acceptance; maximum waiting jobs |
| `MAX_CONCURRENT_INVESTIGATIONS` | `4` | valid active leases across replicas sharing the database |
| `RATE_LIMIT_RPS` | `5` | per-IP rate limit |
| `GRAFANA_ADMIN_PASSWORD` | `admin` | `monitoring` profile only |

**Simulation.** Each investigation gets its own fake cluster built around the
incident's namespace and name, and the real tools run against it.

| Scenario | The cluster shows | Rules verdict |
|---|---|---|
| `healthy` | 2/2 ready, no warnings | no Kubernetes-level fault |
| `crash_loop_backoff` | exit 1, config error in previous logs | container restarts with errors |
| `image_pull_error` | ImagePullBackOff, `manifest unknown` | image cannot be pulled |
| `oom_killed` | OOMKilled (137), memory at 98% of limit | OOMKilled: memory exhaustion |
| `readiness_probe_failure` | running, not ready, probe 503 | readiness probe failure |
| `liveness_probe_failure` | restarts, `Killing` events | liveness probe failure |
| `missing_configmap` | CreateContainerConfigError | referenced ConfigMap is missing |
| `service_selector_mismatch` | healthy pods, Service has no endpoints | Service selector mismatch |
| `dns_failure` | `no such host` in logs | DNS resolution failure |
| `resource_pressure` | Pending, `Insufficient memory` | pods cannot be scheduled |
| `network_policy_block` | egress only to DNS, `i/o timeout` to the DB | possible NetworkPolicy restriction; enforcement unconfirmed |

Live mode rejects a `scenario` in the request.

**Diagnostics:** `get_namespace`, `get_deployment`, `get_pods`, `get_events`,
`get_pod_logs`, `get_service`, `get_configmap`, `get_secret_metadata`,
`get_network_policies`, `get_resource_usage`. Evidence is bounded: the last 30
lines plus error lines per log stream, 50 events, 20 pods with unhealthy ones
first. Events match the exact target, its pods or its ReplicaSets, so `payment`
never picks up `payment-worker`.

**Authentication:** `AUTH_ENABLED=false` supports local/test/minikube without SSO.
Enable it with the [OIDC setup](docs/authentication.md); the backend checks each
request, including direct requests to its port. Browser sessions use cookies;
API and metrics clients use JWTs. An unavailable auth proxy returns `503`.
`/health` and `/ready` remain public; readiness also checks auth when enabled.

## API

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/incidents` | `cluster`, `namespace`, `resource_type` (`deployment`, `service`, `pod`), `resource_name`, `description`, optional `title` |
| `GET` | `/api/v1/incidents?limit=&offset=` | newest first, max 100 |
| `GET` | `/api/v1/incidents/{id}` | one incident |
| `POST` | `/api/v1/incidents/{id}/jobs` | optional `{"scenario": "..."}`; UUID `Idempotency-Key` required; `202` + `Location` |
| `GET` | `/api/v1/incidents/{id}/jobs/{jobID}` | job state and current attempt with saved progress |
| `POST` | `/api/v1/incidents/{id}/investigate` | same queue, waits for `201`/`502`; optional UUID `Idempotency-Key` |
| `GET` | `/api/v1/incidents/{id}/investigations?limit=&offset=` | all attempts, newest first, default 20/max 100, with `has_more` |
| `GET` | `/api/v1/incidents/{id}/investigations/{investigationID}` | one attempt, including failed/running attempts without a report |
| `GET` | `/api/v1/incidents/{id}/report` | latest completed investigation with evidence and report |
| `GET` | `/api/v1/runtime` | `mode`, `cluster_name`, `allowed_namespaces`, `analyzer` (`provider`, `model`), `scenarios` (simulation only); no URLs or keys; requires login when auth is on |
| `GET` | `/health`, `/ready`, `/metrics` | liveness, readiness (database and Kubernetes `/version`), Prometheus |

API errors are `{"error": {"code", "message"}, "request_id"}`: `400`
`validation_failed` / `invalid_json` (including a target outside the policy),
`404` `not_found`, `413` `body_too_large` (over 1 MiB), `429` `busy` /
`rate_limited` with `Retry-After`, `409` `idempotency_conflict`, `503` `unavailable`
for job storage errors, `502` `analysis_failed` with stored evidence on the waiting endpoint.
Jobs survive client disconnects. See [queue, retries and upgrade instructions](docs/durable-jobs.md).
Logs are JSON with request and trace IDs. The `monitoring` profile
provisions a Grafana dashboard from [deployments/grafana/](deployments/grafana/).
The UI checks job status every 2 seconds and resumes the saved job after a tab reload. It resends a `rate_limited` request after `Retry-After` plus up to 0.5 s jitter until the request's 15 s timeout; `busy` is shown. Select older attempts from history; a failed attempt keeps its evidence and does not replace the latest successful report, and an older attempt is labelled with its age. The incident list shows 20 per page.
**Export** saves the selected attempt as Markdown or JSON from the data the API returned (already masked). JSON is the attempt record plus `incident` and `export: {exported_at, build, schema_version}` (`1` for legacy reports, `null` without a report); legacy scores are never exported as numbers.
New analyses use [report schema v2](docs/analysis-contract.md): `verdict`, evidence `support`, exact citations, missing data and scope limits. New API clients must handle v2 instead of `confidence`; stored legacy reports remain readable.
`/health` and `/ready` also expose `build: {version, revision, built_at}` publicly,
including a failing readiness response. The UI shows the responding replica's
version and short revision. See [build metadata](docs/build-metadata.md) for defaults and build arguments.

## Data deletion

`./bin/api purge --before 2026-01-01T00:00:00Z` previews old incidents. Deletion
requires the preview token; active investigations are skipped. Nothing is deleted
automatically. See [operator commands and backup limits](docs/data-deletion.md).

## Kubernetes deployment

```bash
kubectl apply -k deployments/kubernetes
kubectl -n troubleshooter create secret generic troubleshooter-secrets \
  --from-literal=DATABASE_URL='postgres://...' --from-literal=OPENAI_API_KEY=''
```

For each additional namespace, add a RoleBinding in
[rbac.yaml](deployments/kubernetes/rbac.yaml) and extend
`KUBERNETES_ALLOWED_NAMESPACES` in `configmap.yaml`. PostgreSQL is external.
The kustomization has the Namespace (Pod Security `restricted`), ServiceAccount,
RBAC, ConfigMap (live mode), Deployment (2 replicas, non-root, read-only root
filesystem, no capabilities, `RuntimeDefault` seccomp), Service and Ingress.

RBAC grants, per namespace through a RoleBinding: `get, list` on pods,
services; `get` on `pods/log`, configmaps, deployments; `list` on events,
endpointslices, networkpolicies and `metrics.k8s.io/pods`; plus `get` on
namespaces. Nothing else: no write verbs, `pods/exec`, `attach`, `portforward`
or Secrets. `get secrets` (used as metadata only) is in the opt-in
[optional/secret-metadata-rbac.yaml](deployments/kubernetes/optional/secret-metadata-rbac.yaml);
without it, or without metrics-server, that diagnostic is recorded as failed and
the investigation continues. If the target itself cannot be read, the
investigation fails rather than diagnose something it never saw.

Before upgrading, read [operations](docs/operations.md): stopping replicas across
schema changes, `pg_dump`/`pg_restore`, rollback and a runbook for common failures.

## Limitations

- Authentication is off by default for local/test use. Enable it before exposing incident data; all admitted users share the installation's incidents.
- Workloads are Deployments, Services and Pods only: no StatefulSets, DaemonSets, Jobs, HPA, PVCs, Ingress or node diagnostics.
- One cluster per installation; replicas must use the same limits, target policy and analyzer. No remediation is executed.
- The UI needs browser session storage for safe submission retries and a browser supporting `AbortSignal.timeout`/`any`. It watches one job at a time; jobs from other sessions are discovered on loaded history pages.
- An interrupted LLM request is never automatically repeated; its outcome can be unknown and billed without a stored report. Legacy `running` rows are not automatically repaired.
- Live CI uses single-node kind on Kubernetes 1.36.4 and 1.37.0 (pull requests: 1.37.0 only), Linux amd64. Other CNI implementations, clouds and cluster OIDC/TLS/Ingress are not covered. The OpenAI and Ollama clients use fake servers in CI. Browser tests run in Chromium only; Firefox and Safari are untested.
- Redaction uses patterns, not a guarantee of removing every secret; identifiers remain visible. Base64 lines and similar blobs may also be masked. Older rows and backups are not rewritten; API reads mask older data.
- Accessibility is checked with axe's automated rules in Chromium only; there is no manual screen-reader audit.
- Exports are created in the browser; the server does not record who exported what.
- The LLM input limit counts JSON bytes, not tokens; it does not guarantee a model context fit.
- Upgrades are tested from commit `06db1ed` (schema 1), not from a published release. The application refuses to start on a newer database schema; roll back by restoring the pre-upgrade backup. Builds before this check lack it.
- Citation validation checks membership, not the truth of model prose. After v2 reports exist, schema downgrade is blocked; see [upgrade and rollback](docs/analysis-contract.md#upgrade-and-rollback).

## Development

```bash
make test               # unit tests with the race detector
make test-auth          # real OAuth2 Proxy + disposable OIDC provider; requires Docker
make test-integration   # starts a disposable PostgreSQL container, runs -tags integration
make test-e2e           # disposable kind: live diagnostics, RBAC and NetworkPolicy
make test-upgrade       # baseline upgrade, two-replica migration, pg_dump/pg_restore; requires Docker
make test-browser       # Playwright/Chromium on the real binary; needs Docker and `npx playwright install chromium`
make lint               # gofmt, golangci-lint (includes go vet checks)
cd web && npm test && npm run build  # UI render/HTTP workflow tests, typecheck and build (Node 22)
```

Unit tests use synthetic clusters and model responses; integration tests exercise
PostgreSQL, API requests and worker recovery. The upgrade test compares API reads
before and after a real backup and restore. The browser tests drive the embedded UI
in Chromium: a report with evidence links, a reload during a failing analysis,
keyboard-only use and status-read errors. They also run axe's automated WCAG 2.1 A/AA
rules on the main states in light and dark mode, and check that long titles do not
widen the page at 390 and 1280 px. See [evaluation commands](docs/evaluation.md)
for offline checks and the opt-in OpenAI/Ollama tool. The [kind tests](docs/kubernetes-testing.md)
deploy two API replicas and check live diagnostics, RBAC and NetworkPolicy using
one shared image in CI. CI also runs govulncheck and `npm audit`.

Built with an AI coding assistant. I wrote the spec and reviewed every change.

## License

MIT, see [LICENSE](LICENSE).
