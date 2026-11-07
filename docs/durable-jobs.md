# Durable investigation jobs

Accepted work is stored in PostgreSQL. HTTP request cancellation stops only the
client's wait. Workers in each application process claim work from the same
queue; a separate broker is not needed.

## Submit and read

Create an incident, then submit its analysis with a UUID key:

```bash
curl -i -X POST "http://localhost:8080/api/v1/incidents/$ID/jobs" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: 7a739aa4-0135-4355-a267-98d49db41dab' \
  -d '{"scenario":"oom_killed"}'
```

A new submission returns `202 Accepted` and `Location:
/api/v1/incidents/{id}/jobs/{jobID}`. GET that location for `200` with:

| Field | Meaning |
|---|---|
| `job` | stable `id`, `incident_id`, `status`, `stage`, `attempts`, `created_at`, `deadline_at`, `next_run_at`, optional `completed_at`, `error`, `scenario` |
| `result` | current `investigation`, `tool_executions`, `evidence`, optional `report` |
| `status` | `queued`, `running`, `completed`, `failed` |
| `stage` | `queued`, `diagnostics`, `analyzing`, `completed`, `failed` |

Bodies use the existing optional `scenario`; live mode rejects it. Both path
IDs and the key must be UUIDs. Keys are case insensitive and scoped to one
incident. Reusing a key with the same requested options returns the same job:
`202` while active, `200` when terminal. Changing options returns `409
idempotency_conflict`. Lookup precedes capacity checks, so a full queue does not
block replay. Keys remain until the incident is deleted; they grant no access.
Existing authentication, CSRF, rate limiting and masking apply to both routes.
A foreign job ID returns `404`.

Use a new key for an intentional new analysis. Reuse the original key after an
uncertain submission response. `POST .../investigate` waits on this same queue,
returning its previous record shape with `201` or `502 analysis_failed`. Its key
is optional; without one, every call creates a new job, including network
retries. Its `Location` identifies the accepted job. Configure client and proxy
response timeouts for queue wait plus execution, or use the asynchronous routes.
The UI uses the asynchronous routes described below.

## Browser UI

The UI checks one job at a time and keeps the latest results visible if a read
fails. **Retry status check** only reads status. After an uncertain submission,
**Retry submission** reuses the saved key and scenario; a deliberate new analysis
uses a new key. Reloading the tab resumes observation. Browser session storage
must be available; clearing it can lose an unconfirmed key, so inspect history
before submitting from another tab. Leaving the view does not cancel server work.

Select an older attempt to inspect its own diagnostics, error or report. A failed
attempt does not replace **Latest successful report**. Selecting history does not
start or retry an investigation. The relevant API reads are:

| Endpoint suffix after `/api/v1/incidents/{id}` | Result |
|---|---|
| `/investigations?limit=20&offset=0` | `{investigations, offset, limit, has_more}`; default 20, max 100, non-negative offset |
| `/investigations/{investigationID}` | `{investigation, tool_executions, evidence, report?}`; a failed/running/queued attempt may have no report |
| `/report` | newest completed attempt with a report; `404` if none exists |

History is newest first (`started_at`, then ID). New attempts can shift offset
pages. Invalid IDs/paging return `400`; missing or foreign attempts return `404`.

## Limits and recovery

| Setting | Default | Scope |
|---|---|---|
| `JOB_QUEUE_CAPACITY` | `100` | waiting jobs across the database, including retry backoff; active jobs excluded |
| `MAX_CONCURRENT_INVESTIGATIONS` | `4` | running jobs with a valid database lease across replicas |
| `JOB_TIMEOUT` | `15m` | queue, backoff and all attempts from acceptance |
| `INVESTIGATION_TIMEOUT` | `360s` | one attempt |
| Lease / heartbeat | `30s` / `10s` | fixed; failed renewal cancels that worker |
| Execution attempts | at most `3` | first attempt included; backoff `1s`, then `2s` |

A full queue rejects new work with `429 busy` and `Retry-After: 10`; a job storage
error returns `503 unavailable`. PostgreSQL serializes enqueue, claim, recovery
and completion decisions. All replicas must use the same database, limits,
cluster, target policy and analyzer configuration. The limit covers valid leases;
a remote server may finish a request already sent by a worker that loses contact.

Each completed tool step saves masked tool output and evidence atomically.
Writes require the current, unexpired owner token. A late worker cannot save
progress or a report for a reclaimed attempt. Recovery before analysis creates
a new investigation ID and reruns read-only diagnostics from fresh cluster
reads. Earlier evidence stays with its own failed attempt; history exposes
`job_id` and `execution_attempt`. No selectors are reconstructed from masked data.

Recognized transient target-read failures (`429`, `5xx`, timeout) and lost workers
before analysis can retry. Invalid targets, policy failures and denied reads do
not retry. Optional diagnostic failures continue to the analyzer as before.
If the waiting queue is full at retry time, the job ends visibly failed instead
of exceeding capacity. Deadline or attempt exhaustion also ends it failed.

The worker saves `analyzing` before calling the analyzer. Once saved, a timeout,
provider error, invalid result or interrupted worker ends the job without an
automatic analyzer retry. An interrupted request with no committed report has
an unknown outcome; a paid call can have completed without an available report.
A new user submission is needed to try again. This is not an exactly-once promise
for a remote LLM. A conservative unknown outcome is possible even if the process
stopped between saving `analyzing` and sending the request.

When PostgreSQL is unavailable, progress and lease renewal cannot be confirmed;
the worker stops. Recovery checks the deadline when the database returns. A
terminal state cannot be persisted during a database outage or while all workers
are stopped. Legacy `running` rows without `job_id` remain unchanged.

## Upgrade and rollback

Drain requests using the old application version, then stop **all** old processes
before starting this version. Back up PostgreSQL first ([backup and restore](operations.md#backup-and-restore)). Startup applies the
additive migration; existing reports and legacy attempts remain readable. Do not
mix old and new workers during this upgrade: the old version has only a local
concurrency limit and cannot participate in lease ownership. This applies to
Compose and Kubernetes; the supplied Deployment alone does not drain old requests.

To downgrade, stop accepting submissions, let the queue drain, then stop every
worker. Follow the [report v2 rollback constraints](analysis-contract.md#upgrade-and-rollback)
first. `api migrate down` reverts one migration at a time. Reverting the durable-job
migration rejects queued/running jobs and rolls back the whole transaction. With
only terminal jobs, that migration removes job metadata and idempotency keys while
keeping investigations, evidence and reports. Restoring a database backup has its
usual data-loss implications.

Operator deletion skips active jobs and removes old terminal jobs with their
incident; see [data deletion](data-deletion.md).

## Metrics

`jobs_queue_depth` and `jobs_active` are database-wide
gauges sampled every five seconds by each replica: aggregate replicas with `max`,
not `sum`. `job_wait_seconds` measures time from job
acceptance to each claim (including retries). `job_recoveries_total`
counts retried/failed recovery decisions; `jobs_finished_total`
counts terminal outcomes. Counters belong to the worker performing the decision
and can be summed. Labels contain outcomes, never job or incident IDs.
