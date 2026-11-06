# Configuration reference

All settings are environment variables; the app refuses to start on invalid values
and reports every problem at once. Several clusters are configured with a file,
see [several clusters](clusters.md).

## Variables

| Variable | Default | Meaning |
|---|---|---|
| `APP_ENV`, `HTTP_PORT`, `LOG_LEVEL` | `development`, `8080`, `info` | |
| `AUTH_ENABLED` | `false` | no login/provider needed when off; checks UI, API and metrics through OAuth2 Proxy when on |
| `AUTH_PROXY_URL` | `http://127.0.0.1:4180` | internal OAuth2 Proxy origin; Compose uses its own service address |
| `AUTH_PUBLIC_URL` | empty | required when auth is on; HTTPS origin, or HTTP loopback for local auth tests |
| `DATABASE_URL` | | PostgreSQL; Compose points it at its own container |
| `EXTERNAL_DATABASE_URL` | empty | Compose only: use this PostgreSQL instead; start with `up --no-deps app` |
| `EXTERNAL_DATABASE_NETWORK` | | Compose with `docker-compose.external-db.yml`: existing Docker network of that database |
| `POSTGRES_PORT` | `5432` | Compose only: host port of the bundled PostgreSQL, bound to `127.0.0.1` for `make run` |
| `SIMULATION_ENABLED` | `true` | simulated clusters instead of a real one |
| `SIMULATION_SCENARIO` | `healthy` | default scenario |
| `KUBECONFIG`, `KUBERNETES_CONTEXT` | empty | live mode; empty uses `~/.kube/config`, or the in-cluster ServiceAccount in a pod |
| `KUBERNETES_CLUSTER_NAME` | `local` | the only value accepted in an incident's `cluster` |
| `KUBERNETES_CLUSTERS_FILE` | empty | YAML list of clusters, each with its kubeconfig, context and allowed namespaces; replaces `KUBERNETES_CLUSTER_NAME`, `KUBERNETES_CONTEXT` and `KUBERNETES_ALLOWED_NAMESPACES` ([several clusters](clusters.md)) |
| `KUBERNETES_ALLOWED_NAMESPACES` | empty (all readable) | comma-separated allowlist, checked at creation and before each investigation |
| `KUBERNETES_CUSTOM_RESOURCES` | empty | extra kinds as `group/version/resource`, comma-separated, e.g. `argoproj.io/v1alpha1/rollouts`; incidents name them `rollouts.argoproj.io` (see [custom resources](#custom-resources)) |
| `LLM_PROVIDER` | `rules` | `rules`, `openai`, `anthropic`, `ollama` |
| `LLM_MODEL` | per provider | `gpt-4o-mini`, `claude-opus-5-5`, `llama3.2` |
| `LLM_REASONING_EFFORT` | empty | OpenAI reasoning models (e.g. `gpt-6-luna`): `low`, `medium`, `high`, sent instead of temperature 0, which they reject; Anthropic: `output_config.effort` (`low` to `max`), empty uses the model default |
| `LLM_MAX_INPUT_BYTES` | `262144` | entire serialized provider request body; overflow fails analysis without sending, preserving evidence |
| `LLM_TIMEOUT` | `60s` | per LLM request |
| `OPENAI_API_KEY`, `OPENAI_BASE_URL` | | any OpenAI-compatible API |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Claude Messages API; no temperature is sent, and thinking counts against the 16000-token output limit |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | |
| `INVESTIGATION_TIMEOUT` | `90s` | per execution attempt |
| `JOB_TIMEOUT`, `JOB_QUEUE_CAPACITY` | `15m`, `100` | total time from acceptance; maximum waiting jobs |
| `MAX_CONCURRENT_INVESTIGATIONS` | `4` | valid active leases across replicas sharing the database |
| `RATE_LIMIT_RPS` | `5` | per-IP rate limit |

## Simulation

Each investigation gets its own fake cluster built around the
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

Each scenario builds the target's kind: a StatefulSet, DaemonSet, Job or CronJob
(with one active run) target gets the same pods and fault; an Ingress, claim or
autoscaler target leads to that workload. Live mode rejects a
`scenario` in the request.

## Diagnostics

The diagnostics are `get_namespace`, `get_deployment`, `get_statefulset`,
`get_daemonset`, `get_job`, `get_cronjob` (and its latest Job), `get_ingress` (backend
Services and ports, published address), `get_hpa` (then its Deployment or StatefulSet),
`get_pvc` (the target or each mounted claim), `get_nodes` (nodes running the pods:
conditions, taints, capacity; no addresses), `get_pods`, `get_events`,
`get_pod_logs`, `get_service`, `get_configmap`, `get_secret_metadata`,
`get_network_policies`, `get_resource_usage`. Evidence is bounded: the last 30
lines plus error lines per log stream, 50 events, 20 pods with unhealthy ones
first. Events match the exact target, its pods or its ReplicaSets, so `payment`
never picks up `payment-worker`. The rules analyzer quotes the last logged error as an
observation (not a confirmed cause), names missing ConfigMaps or Secrets, and fills
known pod and Service names into its read-only commands.

## Custom resources

Each `KUBERNETES_CUSTOM_RESOURCES` entry becomes a Kind in
the form. `get_custom_resource` keeps only metadata, `status.conditions`,
`status.phase`, the generations and the pod selector (`spec.selector.matchLabels`
or `status.selector`); no other spec or status value is stored. A failing condition
(`Ready`, `Available`, `Healthy`, `Synced`, `Succeeded`, `Progressing`, `Reconciled`
False; `Degraded`, `Failed`, `Stalled`, `Error`, `ReconcileError` True) or a failed
phase is a finding. Then the selected pods run through the usual checks. Grant
read access per namespace, as [the e2e fixture](../tests/e2e/custom-resource.yaml) does:

```yaml
rules:
  - apiGroups: ["argoproj.io"]
    resources: ["rollouts"]
    verbs: ["get", "list"]
```

## Authentication

`AUTH_ENABLED=false` supports local/test/minikube without SSO.
Enable it with the [OIDC setup](authentication.md); the backend checks each
request, including direct requests to its port. Browser sessions use cookies;
API and metrics clients use JWTs. An unavailable auth proxy returns `503`.
`/health` and `/ready` remain public; readiness also checks auth when enabled.
