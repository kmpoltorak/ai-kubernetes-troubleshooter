# AI Kubernetes Troubleshooter

[![CI](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml/badge.svg)](https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/actions/workflows/ci.yml)

A Go service that investigates broken Kubernetes workloads (Deployment, Service
or Pod). It runs a fixed, read-only diagnostic plan against the cluster, turns
the results into structured evidence with signals such as `OOMKilled` or
`SelectorMismatch`, and has an LLM or a built-in rules engine write a
root-cause report, which is validated before it is stored. It runs offline
against simulated clusters, or live with a read-only ServiceAccount.

```text
$ ID=$(curl -s -X POST localhost:8080/api/v1/incidents -d @docs/examples/oom_killed.json | jq -r .id)
$ curl -s -X POST localhost:8080/api/v1/incidents/$ID/investigate -d '{"scenario":"oom_killed"}' \
    | jq '.report.analysis | {root_cause, severity, confidence, safe_commands}'
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

- The LLM gets data only: no tools, shell or Kubernetes client. The 10 diagnostics are a fixed allowlist calling only `Get`, `List` and `GetLogs`, and every tool input comes from earlier results, never from model output.
- Secret values are never read: Secrets are fetched as metadata only, env vars are reported as names and source kinds, ConfigMaps as key names and sizes.
- Passwords, tokens, JWTs, API keys, URL credentials and private keys are redacted from logs and events before storage or analysis, including multi-line PEM blocks.
- A report may cite only evidence that was collected and suggest only read-only `kubectl` commands (no Secret access, shell syntax, line breaks, `--as`, `--token` or `--raw`). Invalid output fails the investigation with `502` and keeps the evidence.
- A diagnostic that fails is passed to the analyzer as failed; the rules engine never calls a workload healthy when pods, events or logs could not be read.

## Install

Requires Docker with Compose. Go 1.27+ (and Node 22 for the UI) only to run
outside Docker.

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

The UI's sample buttons fill in an incident and pick the matching simulation
scenario. Outside Docker:

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
| `DATABASE_URL` | | PostgreSQL; Compose points it at its own container |
| `SIMULATION_ENABLED` | `true` | simulated clusters instead of a real one |
| `SIMULATION_SCENARIO` | `healthy` | default scenario |
| `KUBECONFIG`, `KUBERNETES_CONTEXT` | empty | live mode; empty uses `~/.kube/config`, or the in-cluster ServiceAccount in a pod |
| `KUBERNETES_CLUSTER_NAME` | `local` | the only value accepted in an incident's `cluster` |
| `KUBERNETES_ALLOWED_NAMESPACES` | empty (all readable) | comma-separated allowlist, checked at creation and before each investigation |
| `LLM_PROVIDER` | `rules` | `rules`, `openai`, `ollama` |
| `LLM_MODEL` | per provider | `gpt-4o-mini`, `llama3.2` |
| `LLM_TIMEOUT` | `60s` | per LLM request |
| `OPENAI_API_KEY`, `OPENAI_BASE_URL` | | any OpenAI-compatible API |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | |
| `INVESTIGATION_TIMEOUT` | `90s` | |
| `MAX_CONCURRENT_INVESTIGATIONS` | `4` | more get `429 busy` |
| `RATE_LIMIT_RPS` | `5` | per-IP rate limit |
| `GRAFANA_ADMIN_PASSWORD` | `admin` | `monitoring` profile only |

**Simulation.** Each investigation gets its own fake cluster built around the
incident's namespace and name, and the real tools run against it.

| Scenario | The cluster shows | Rules verdict |
|---|---|---|
| `healthy` | 2/2 ready, no warnings | no Kubernetes-level fault |
| `crash_loop_backoff` | exit 1, config error in previous logs | application startup failure |
| `image_pull_error` | ImagePullBackOff, `manifest unknown` | image cannot be pulled |
| `oom_killed` | OOMKilled (137), memory at 98% of limit | OOMKilled: memory exhaustion |
| `readiness_probe_failure` | running, not ready, probe 503 | readiness probe failure |
| `liveness_probe_failure` | restarts, `Killing` events | liveness probe failure |
| `missing_configmap` | CreateContainerConfigError | referenced ConfigMap is missing |
| `service_selector_mismatch` | healthy pods, Service has no endpoints | Service selector mismatch |
| `dns_failure` | `no such host` in logs | DNS resolution failure |
| `resource_pressure` | Pending, `Insufficient memory` | insufficient cluster resources |
| `network_policy_block` | egress only to DNS, `i/o timeout` to the DB | NetworkPolicy blocks egress |

Live mode rejects a `scenario` in the request.

**Diagnostics:** `get_namespace`, `get_deployment`, `get_pods`, `get_events`,
`get_pod_logs`, `get_service`, `get_configmap`, `get_secret_metadata`,
`get_network_policies`, `get_resource_usage`. Evidence is bounded: the last 30
lines plus error lines per log stream, 50 events, 20 pods with unhealthy ones
first. Events match the exact target, its pods or its ReplicaSets, so `payment`
never picks up `payment-worker`.

## API

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/incidents` | `cluster`, `namespace`, `resource_type` (`deployment`, `service`, `pod`), `resource_name`, `description`, optional `title` |
| `GET` | `/api/v1/incidents?limit=&offset=` | newest first, max 100 |
| `GET` | `/api/v1/incidents/{id}` | one incident |
| `POST` | `/api/v1/incidents/{id}/investigate` | optional `{"scenario": "..."}` |
| `GET` | `/api/v1/incidents/{id}/report` | latest completed investigation with evidence and report |
| `GET` | `/health`, `/ready`, `/metrics` | liveness, readiness (database and Kubernetes `/version`), Prometheus |

Errors are `{"error": {"code", "message"}, "request_id"}`: `400`
`validation_failed` / `invalid_json` (including a target outside the policy),
`404` `not_found`, `413` `body_too_large` (over 1 MiB), `429` `busy` /
`rate_limited` with `Retry-After`, `502` `analysis_failed` with the stored
evidence. Logs are JSON with request and trace IDs. The `monitoring` profile
provisions a Grafana dashboard from [deployments/grafana/](deployments/grafana/).

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

## Limitations

- No API authentication: expose it only behind an authenticating ingress or on an internal network.
- Workloads are Deployments, Services and Pods only: no StatefulSets, DaemonSets, Jobs, HPA, PVCs, Ingress or node diagnostics.
- Investigations are synchronous, one cluster per instance; no remediation is ever executed.
- Live mode was checked by hand on minikube only; CI uses no real cluster. The OpenAI and Ollama clients are tested against fake servers.
- Redaction masks runs of base64 lines, so hex digests and similar blobs in logs are masked too.

## Development

```bash
make test               # unit tests with the race detector
make test-integration   # starts a disposable PostgreSQL container, runs -tags integration
make lint               # gofmt, go vet, golangci-lint
```

Each tool is tested against client-go fake clients, and all 10 tools run
against all 11 scenarios checking the expected signals and the absence of
misleading ones. An evaluation runs every scenario end to end through the
rules analyzer and checks root cause and severity. Integration tests run
migrations and send the sample incidents through REST, the engine and
PostgreSQL. Go tests also fail if the RBAC manifest gains a permission or a
Grafana panel queries a metric the app does not export. CI adds govulncheck,
`npm audit` and a Docker build. A report from a live minikube run is in
[docs/examples/report_oom_killed_minikube.json](docs/examples/report_oom_killed_minikube.json).

Built with an AI coding assistant. I wrote the spec and reviewed every change.

## License

MIT, see [LICENSE](LICENSE).
