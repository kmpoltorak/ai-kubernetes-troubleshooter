# AI Kubernetes Troubleshooter

[![CI](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml/badge.svg)](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml)

A Go service that investigates broken Kubernetes workloads (Deployment,
StatefulSet, DaemonSet, Job, CronJob, Service, Pod, Ingress,
PersistentVolumeClaim or HorizontalPodAutoscaler). It runs a fixed, read-only diagnostic plan against the cluster, turns
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
- Failed diagnostics and sampling limits remain in the report. New reports cannot claim `no_fault_observed` when core diagnostics failed or collected observations are unhealthy, and recommend nothing when no fault is observed; support is qualitative, never a probability.

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
LLM_PROVIDER=ollama docker compose up --build      # analysis by Ollama running on the host
```

Live cluster from Docker, with a kubeconfig for a read-only ServiceAccount
(the [RBAC](docs/operations.md#rbac); readable by the container's uid 65532):

```bash
KUBECONFIG_FILE=$HOME/.kube/troubleshooter.yaml KUBERNETES_CLUSTER_NAME=lab \
  KUBERNETES_ALLOWED_NAMESPACES=payments \
  docker compose -f docker-compose.yml -f docker-compose.live.yml up --build
```

Several clusters from one installation: [several clusters](docs/clusters.md).
An existing PostgreSQL instead of the bundled one (the app applies its migrations
at start; for a database container on the same host see `EXTERNAL_DATABASE_NETWORK`
in [configuration](docs/configuration.md)):

```bash
EXTERNAL_DATABASE_URL='postgres://troubleshooter:PASSWORD@db.example.net:5432/troubleshooter?sslmode=require' \
  docker compose up --build -d --no-deps app
```

In simulation, the UI's sample buttons fill in an incident and pick a scenario.
In live mode the form offers the configured clusters and namespaces and suggests
object names from the cluster. A "?" next to a field or report term explains it.
A report opens with its conclusion, cited evidence and export buttons. Outside Docker:

```bash
docker compose up -d postgres
make web    # optional: builds the UI into the binary
make run    # applies migrations, serves on :8080
SIMULATION_ENABLED=false KUBERNETES_CONTEXT=my-cluster KUBERNETES_ALLOWED_NAMESPACES=payments make run   # live
```

## Configuration

The main settings; all variables, simulation scenarios, diagnostics and custom
resources are in the [configuration reference](docs/configuration.md).

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | | PostgreSQL; Compose points it at its own container |
| `SIMULATION_ENABLED` | `true` | simulated clusters instead of a real one |
| `KUBECONFIG`, `KUBERNETES_CONTEXT` | empty | live mode; empty uses `~/.kube/config`, or the in-cluster ServiceAccount in a pod |
| `KUBERNETES_CLUSTER_NAME` | `local` | the only value accepted in an incident's `cluster` |
| `KUBERNETES_ALLOWED_NAMESPACES` | empty (all readable) | comma-separated allowlist, checked at creation and before each investigation |
| `KUBERNETES_CLUSTERS_FILE` | empty | several clusters, each with its kubeconfig and namespaces ([several clusters](docs/clusters.md)) |
| `LLM_PROVIDER`, `LLM_MODEL` | `rules`, per provider | `rules`, `openai` (`gpt-4o-mini`), `ollama` (`llama3.2`) |
| `OPENAI_API_KEY` | | required for `openai`; environment only |
| `AUTH_ENABLED` | `false` | login through OAuth2 Proxy for UI, API and metrics ([authentication](docs/authentication.md)) |

## API

The main endpoints; the full list, errors and UI behaviour are in the
[API reference](docs/api.md).

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/incidents` | `cluster`, `namespace`, `resource_type`, `resource_name`, `description`, optional `title` |
| `POST` | `/api/v1/incidents/{id}/jobs` | queue an investigation; UUID `Idempotency-Key` required; `202` + `Location` |
| `POST` | `/api/v1/incidents/{id}/investigate` | same queue, waits for `201`/`502` |
| `GET` | `/api/v1/incidents/{id}/report` | latest completed investigation with evidence and report ([schema v2](docs/analysis-contract.md)) |
| `GET` | `/health`, `/ready`, `/metrics` | liveness, readiness, Prometheus |

## Data deletion

In the UI, tick incidents and choose **Delete selected**, or **Delete incident** in
the detail view; a dialog states the count and there is no undo. Incidents with a
queued or running investigation are skipped. `./bin/api purge --before <time>`
previews and deletes old incidents with a token; nothing is deleted automatically.
See [data deletion](docs/data-deletion.md).

## Kubernetes deployment

```bash
kubectl apply -k deployments/kubernetes
kubectl -n troubleshooter create secret generic troubleshooter-secrets \
  --from-literal=DATABASE_URL='postgres://...' --from-literal=OPENAI_API_KEY=''
```

PostgreSQL is external. The kustomization has the Namespace (Pod Security
`restricted`), ServiceAccount, read-only [RBAC](docs/operations.md#rbac), ConfigMap
(live mode), Deployment (2 replicas, non-root, read-only root filesystem, no
capabilities, `RuntimeDefault` seccomp), Service
and Ingress. For each additional namespace, add a RoleBinding in
[rbac.yaml](deployments/kubernetes/rbac.yaml) and extend
`KUBERNETES_ALLOWED_NAMESPACES`. Before upgrading, read [operations](docs/operations.md);
for a digest-pinned release see [releases](docs/releases.md).

## Limitations

- Authentication is off by default for local/test use. Enable it before exposing incident data; all admitted users share the installation's incidents.
- Custom resources are judged by the [condition names listed in configuration](docs/configuration.md#custom-resources); a controller using other names is not interpreted, and a resource without conditions or phase is `inconclusive`. Pods are found only through a label selector, not owner references. A configured resource the cluster does not serve fails its investigations. A CronJob is judged by its latest Job only (found among at most 500 Jobs in the namespace); missed schedules are not detected. An Ingress is followed to its first existing backend's pods; an autoscaler only to a Deployment or StatefulSet. A Pending claim no pod uses is not called a fault (WaitForFirstConsumer classes bind later); StorageClasses are not read. Nodes are read only when they run the target's pods, so a Pending pod's candidate nodes are not inspected.
- Clusters of one installation share custom resources, limits and analyzer; replicas must use the same limits, target policy and analyzer. Kubeconfig changes need a restart. No remediation is executed.
- The UI needs browser session storage for safe submission retries and a browser supporting `AbortSignal.timeout`/`any`; the "?" hints need the Popover API (Chrome 114, Safari 17, Firefox 125). It watches one job at a time; jobs from other sessions are discovered on loaded history pages.
- An interrupted LLM request is never automatically repeated; its outcome can be unknown and billed without a stored report. Legacy `running` rows are not automatically repaired.
- Live CI uses single-node kind on Kubernetes 1.37.0, Linux amd64; other Kubernetes versions are untested. Other CNI implementations, clouds and cluster OIDC/TLS/Ingress are not covered. The OpenAI and Ollama clients use fake servers in CI. Browser tests run in Chromium only; Firefox and Safari are untested.
- Redaction uses patterns, not a guarantee of removing every secret; identifiers remain visible. Base64 lines and similar blobs may also be masked. Older rows and backups are not rewritten; API reads mask older data.
- Accessibility is checked with axe's automated rules in Chromium only; there is no manual screen-reader audit.
- Exports are created in the browser; the server does not record who exported what.
- Empty observations count as missing data: no log lines (core) or no usage samples (optional). A healthy workload whose containers write no logs is therefore `inconclusive`, not `no_fault_observed`.
- The LLM input limit counts JSON bytes, not tokens; it does not guarantee a model context fit.
- Upgrades are tested from commit `06db1ed` (schema 1) and from the newest published release, built from its tag's source (not its image). The application refuses to start on a newer database schema; roll back by restoring the pre-upgrade backup. Builds before this check lack it.
- Citation validation checks membership, not the truth of model prose. After v2 reports exist, schema downgrade is blocked; see [upgrade and rollback](docs/analysis-contract.md#upgrade-and-rollback).

## Development

```bash
make test               # unit tests with the race detector
make test-auth          # real OAuth2 Proxy + disposable OIDC provider; requires Docker
make test-integration   # starts a disposable PostgreSQL container, runs -tags integration
make test-e2e           # disposable kind: live diagnostics, RBAC and NetworkPolicy
make test-upgrade       # baseline and last-release upgrades, two-replica migration, pg_dump/pg_restore; requires Docker
make test-browser       # Playwright/Chromium on the real binary; needs Docker and `npx playwright install chromium`
make lint               # gofmt, golangci-lint (includes go vet checks)
cd web && npm test && npm run build  # UI render/HTTP workflow tests, typecheck and build (Node 22)
```

Unit tests use synthetic clusters and model responses; integration tests exercise
PostgreSQL, API requests and worker recovery. The upgrade test upgrades from a
schema-1 commit and from the previous release, and compares API reads before and
after a real backup and restore. The browser tests cover the essential UI flows,
axe WCAG 2.1 A/AA rules in light and dark mode, and a 390 px width. The
[kind tests](docs/kubernetes-testing.md) deploy two replicas and check live
diagnostics, RBAC and NetworkPolicy. CI also runs govulncheck and `npm audit`; the
UI jobs run only when the UI, its API or CI change, and releases run everything.
See [evaluation commands](docs/evaluation.md) for offline checks and the opt-in
OpenAI/Ollama tool.

Built with an AI coding assistant. I wrote the spec and reviewed every change.

## License

MIT, see [LICENSE](LICENSE).
