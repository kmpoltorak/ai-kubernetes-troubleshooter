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

**Run investigation** submits a job and shows its status, stage, attempt number
and saved diagnostics. Status is read every two seconds after the previous
request finishes; requests have a 15-second timeout. Only one read is in flight.
Terminal jobs stop polling. Read errors keep the last results visible; temporary
errors retry automatically, while authentication and other client errors stop
until **Retry status check** or a reload. Retrying status never submits work.

Before submission, the UI saves the UUID key and scenario in this tab's session
storage. A lost response, timeout, malformed response or server error leaves them
available for **Retry submission**, including after a reload. The scenario stays
fixed until that submission is resolved. A known rejection (validation, access,
missing incident, key conflict, body limit or full queue) clears the pending
submission so the user can change options. A deliberate new analysis after a
terminal job gets a new key.

After acceptance, session storage holds only the job ID. Opening that incident
again in the same tab resumes observation. Without a saved job, the UI opens a
queued/running durable attempt from the loaded history page, or the newest
attempt if none is active. Selecting an active attempt on an older page also
starts observation. It watches one job at a time; it does not scan every history
page for other sessions' work. Legacy `running` rows have no job to follow.

History remains selectable during work. Choosing an older completed/failed
attempt keeps that selection while the job status continues updating. **View
current attempt** returns to live progress, including a new attempt after recovery.
History and the latest successful report refresh when an observed attempt changes
or the job finishes; manual refresh remains available.

Leaving the incident aborts local requests and timers, while accepted server work
continues. Closing a tab or clearing session data can remove an unconfirmed key;
inspect history before submitting again in another session. No diagnostic bodies
or credentials are stored in browser session storage. If that storage is blocked
or malformed, new submissions stop with an error rather than losing a retry key.
The UI requires browser support for `AbortSignal.timeout` and `AbortSignal.any`.

## Limits and recovery

| Setting | Default | Scope |
|---|---|---|
| `JOB_QUEUE_CAPACITY` | `100` | waiting jobs across the database, including retry backoff; active jobs excluded |
| `MAX_CONCURRENT_INVESTIGATIONS` | `4` | running jobs with a valid database lease across replicas |
| `JOB_TIMEOUT` | `15m` | queue, backoff and all attempts from acceptance |
| `INVESTIGATION_TIMEOUT` | `90s` | one attempt |
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
before starting this version. Back up PostgreSQL first. Startup applies the
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
