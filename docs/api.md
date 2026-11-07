# API reference

The UI uses the same API. With `AUTH_ENABLED=true`, everything except `/health`
and `/ready` requires login ([authentication](authentication.md)).

## Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/incidents` | `cluster`, `namespace`, `resource_type` (one of `/api/v1/runtime` `resource_types`), `resource_name`, `description`, optional `title` |
| `POST` | `/api/v1/incidents/delete` | `{"ids": [...]}`, 1–100 UUIDs; deletes each incident with all its investigations, evidence, reports and jobs; returns `{deleted, skipped}` (skipped: work in flight or unknown ID) |
| `GET` | `/api/v1/incidents?limit=&offset=` | newest first, max 100 |
| `GET` | `/api/v1/incidents/{id}` | one incident |
| `POST` | `/api/v1/incidents/{id}/jobs` | optional `{"scenario": "..."}`; UUID `Idempotency-Key` required; `202` + `Location` |
| `GET` | `/api/v1/incidents/{id}/jobs/{jobID}` | job state and current attempt with saved progress |
| `POST` | `/api/v1/incidents/{id}/investigate` | same queue, waits for `201`/`502`; optional UUID `Idempotency-Key` |
| `GET` | `/api/v1/incidents/{id}/investigations?limit=&offset=` | all attempts, newest first, default 20/max 100, with `has_more` |
| `GET` | `/api/v1/incidents/{id}/investigations/{investigationID}` | one attempt, including failed/running attempts without a report |
| `GET` | `/api/v1/incidents/{id}/report` | latest completed investigation with evidence and report |
| `GET` | `/api/v1/runtime` | `mode`, `clusters` (`name`, `allowed_namespaces`), `cluster_name` and `allowed_namespaces` (the first cluster), `analyzer` (`provider`, `model`; for API providers also `reasoning_effort`, `max_output_tokens`, `max_input_bytes`, `timeout_seconds`), `limits` (investigation and job timeouts, queue capacity, concurrency, rate limit), `authentication`, `environment`, `log_level`, `scenarios` and `default_scenario` (simulation only), `resource_types` (the form's Kind list); no keys, URLs, kubeconfig contexts or OIDC settings; requires login when auth is on |
| `GET` | `/api/v1/clusters` | `{clusters: [{name, status}]}`, status `ok`, `unreachable`, `unauthorized` (the API server rejected the kubeconfig credentials, `401`) or `simulated`; pings each cluster's API server |
| `GET` | `/api/v1/targets?cluster=&namespace=&kind=` | `{names}`: objects of a supported kind in an allowed namespace for the form (`cluster` may be omitted when only one is configured) (live: listed from the cluster, at most 500; simulation: empty); `502 list_failed` when listing fails |
| `GET` | `/docs/`, `/docs/{guide}` | these guides rendered as HTML; links to files outside `docs/` open on GitHub |
| `GET` | `/health`, `/ready`, `/metrics` | liveness, readiness (database and Kubernetes `/version`; with several clusters, at least one must answer), Prometheus |

## Errors

API errors are `{"error": {"code", "message"}, "request_id"}`: `400`
`validation_failed` / `invalid_json` (including a target outside the policy),
`404` `not_found`, `413` `body_too_large` (over 1 MiB), `429` `busy` /
`rate_limited` with `Retry-After`, `409` `idempotency_conflict`, `503` `unavailable`
for job storage errors, `502` `analysis_failed` with stored evidence on the waiting endpoint.
Jobs survive client disconnects. See [queue, retries and upgrade instructions](durable-jobs.md).

## Observability

Logs are JSON with request and trace IDs. Prometheus metrics are served at `/metrics`
(the Kubernetes Deployment carries `prometheus.io/*` scrape annotations). Import
[the Grafana dashboard](../deployments/grafana/dashboards/kubernetes-troubleshooter.json)
into your Grafana and pick a Prometheus data source in its `Data source` variable.
`/health` and `/ready` also expose `build: {version, revision, built_at}` publicly,
including a failing readiness response. The UI shows the responding replica's
version and short revision. See [build metadata](build-metadata.md) for defaults and build arguments.

## UI behaviour and export

The UI checks job status every 2 seconds and resumes the saved job after a tab reload. It resends a `rate_limited` request after `Retry-After` plus up to 0.5 s jitter until the request's 15 s timeout; `busy` is shown. Select older attempts from history; a failed attempt keeps its evidence and does not replace the latest successful report, and an older attempt is labelled with its age. The incident list shows 20 per page.
**Export** saves the selected attempt as Markdown or JSON from the data the API returned (already masked). JSON is the attempt record plus `incident` and `export: {exported_at, build, schema_version, ai_notice}` (`schema_version` `1` for legacy reports, `null` without a report; `ai_notice` only when a language model wrote the report); legacy scores are never exported as numbers.
New analyses use [report schema v2](analysis-contract.md): `verdict`, evidence `support`, exact citations, missing data and scope limits. New API clients must handle v2 instead of `confidence`; stored legacy reports remain readable.
