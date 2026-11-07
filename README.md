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
    "kubectl describe pod payment-service-7c9d8f6b5-k4m7p -n payments"
  ]
}
```

- The LLM gets data only: no tools, shell or Kubernetes client. The 19 diagnostics are a fixed allowlist calling only `Get`, `List` and `GetLogs`, and every tool input comes from earlier results, never from model output.
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

The guides in [docs/](docs/) are also served by the app at `/docs/` (the **Docs**
link in the UI header), behind the same login as the UI.
In simulation, the UI's sample buttons fill in an incident and pick a scenario.
In live mode the form offers the configured clusters and namespaces and suggests
object names from the cluster. A "?" next to a field or report term explains it.
A report opens with its conclusion, cited evidence and export buttons; one written
by a language model says so and asks you to verify it. The header shows the mode and the model,
and **Settings** shows the running configuration read-only. Outside Docker:

```bash
docker compose up -d postgres
make web    # optional: builds the UI into the binary
make run    # applies migrations, serves on :8080
SIMULATION_ENABLED=false KUBERNETES_CONTEXT=my-cluster KUBERNETES_ALLOWED_NAMESPACES=payments make run   # live
```

## Configuration

The main settings; all variables, simulation scenarios, diagnostics and custom
resources are in the [configuration reference](docs/configuration.md), which also
lists what is [required to start](docs/configuration.md#required-to-start).
Only `DATABASE_URL` is always required.

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | | required; PostgreSQL; Compose points it at its own container |
| `SIMULATION_ENABLED` | `true` | simulated clusters instead of a real one |
| `KUBECONFIG`, `KUBERNETES_CONTEXT` | empty | live mode; empty uses `~/.kube/config`, or the in-cluster ServiceAccount in a pod |
| `KUBERNETES_CLUSTER_NAME` | `local` | the only value accepted in an incident's `cluster` |
| `KUBERNETES_ALLOWED_NAMESPACES` | empty (all readable) | comma-separated allowlist, checked at creation and before each investigation |
| `KUBERNETES_CLUSTERS_FILE` | empty | several clusters, each with its kubeconfig and namespaces ([several clusters](docs/clusters.md)) |
| `LLM_PROVIDER`, `LLM_MODEL` | `rules`, none | `rules`, `openai`, `anthropic`, `gemini`; `LLM_MODEL` is required for the API providers and checked at startup against the provider's model list |
| `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY` | | required for the matching provider; environment only |
| `LLM_REASONING_EFFORT`, `LLM_MAX_OUTPUT_TOKENS` | `medium`, `16000` | sent to every provider (`off` for models without reasoning); the token limit includes thinking |
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
  --from-literal=DATABASE_URL='postgres://...'
```

The ConfigMap uses the `rules` analyzer. For an API provider set `LLM_PROVIDER` and
`LLM_MODEL` there and add its key (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY` or
`GEMINI_API_KEY`) to the Secret.

PostgreSQL is external. The kustomization has the Namespace (Pod Security
`restricted`), ServiceAccount, read-only [RBAC](docs/operations.md#rbac), ConfigMap
(live mode), Deployment (2 replicas, non-root, read-only root filesystem, no
capabilities, `RuntimeDefault` seccomp), Service
and Ingress. For each additional namespace, add a RoleBinding in
[rbac.yaml](deployments/kubernetes/rbac.yaml) and extend
`KUBERNETES_ALLOWED_NAMESPACES`. Before upgrading, read [operations](docs/operations.md);
for a digest-pinned release see [releases](docs/releases.md).

## Limitations

- Authentication is off by default; enable it before exposing incident data. All admitted users see every incident.
- Reports are advice, nothing is executed. A report written by a language model can be wrong: verify it against the cited evidence.
- With OpenAI, Anthropic or Gemini, masked incident and diagnostic data is sent to that provider. Masking uses patterns and can miss a secret.
- Tested on kind with Kubernetes 1.37.0 (Linux amd64) and in Chromium; other versions, clouds and browsers are untested.

All known gaps, per area: [limitations](docs/limitations.md).

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
OpenAI/Anthropic/Gemini tool.

Built with an AI coding assistant. I wrote the spec and reviewed every change.

## License

MIT, see [LICENSE](LICENSE).
