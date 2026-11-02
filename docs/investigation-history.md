# Investigation history

Each investigation is a separate attempt. The UI opens the latest attempt when
an incident is loaded or history is refreshed. Its status, start/finish times,
diagnostic results and failure message remain available after a reload. Choose
an older attempt from the history to inspect it.

The separate **Latest successful report** link opens the newest completed
attempt that has a report. A failed latest attempt does not replace that report.
Every selected attempt shows its own ID and timestamps; a report also shows
its creation time.

## List attempts

```bash
curl -s "http://localhost:8080/api/v1/incidents/$ID/investigations?limit=20&offset=0"
```

An incident with no attempts returns `200`:

```json
{"investigations":[],"offset":0,"limit":20,"has_more":false}
```

Each entry is an investigation header: `id`, `incident_id`, `status`,
`started_at`, optional `scenario`, `completed_at` and `error`. Durable attempts also
have `job_id` and `execution_attempt` (1–3). The list includes `queued`, `running`,
`completed` and `failed` attempts. It does not include diagnostic
bodies or report contents; use the detail endpoint for those.

| Parameter | Behavior |
|---|---|
| `limit` | default 20; non-positive uses default; capped at 100 |
| `offset` | default 0; non-negative integer |
| `has_more` | response field; more attempts exist after this page |

Ordering is `started_at DESC, id DESC`, including deterministic ordering when
start times match. New attempts can shift offset-based pages; refresh history
to return to the newest page. An offset past the end returns an empty array.

## Read an attempt

```bash
curl -s "http://localhost:8080/api/v1/incidents/$ID/investigations/$INVESTIGATION_ID"
```

The result has `investigation`, `tool_executions`, `evidence` and, when present,
`report`. Queued, failed and running attempts return `200` without a report. A failed
attempt retains collected evidence and a masked error message. Collections are
arrays, including empty arrays. Legacy completed attempts without a report are
also readable. The record is read from one database snapshot, so finishing an
attempt during a read does not mix running metadata with a newly saved report.

Both IDs must be UUIDs. A missing incident, missing attempt, or attempt belonging
to another incident returns `404 not_found`. Invalid UUIDs, non-integer paging
parameters and negative offsets return `400 validation_failed`. Authentication,
rate limits and masking follow the other incident endpoints. Reading legacy
records does not rewrite stored data.

## Existing endpoints

`GET /api/v1/incidents/{id}/report` still returns the newest completed attempt
with a report, ordered by start time and ID. It returns `404` if none exists,
even if failed or running attempts exist. Use history for the latest attempt
regardless of its outcome.

`POST /api/v1/incidents/{id}/investigate` remains synchronous: `201` on success,
`502 analysis_failed` with the stored record on failure. It waits for the
[durable queue](durable-jobs.md); disconnecting the client does not stop accepted work.
History does not start, resume or retry an investigation. Each diagnostic step is
saved before the next step; a running attempt may show partial results. Queued
attempts have no diagnostics. Their `started_at` initially records enqueue time
and is replaced by execution start time when claimed. An interrupted legacy
attempt without `job_id` can remain `running`; it is not adopted by the queue.
The UI refreshes history on demand; it does not automatically poll attempts.
