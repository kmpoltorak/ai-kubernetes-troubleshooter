# Operations: upgrade, backup, restore and runbook

For installing a digest-pinned release see [releases](releases.md); for the
Kubernetes manifests and RBAC see the [README](../README.md#kubernetes-deployment).

## Dependencies

| Dependency | Required | Readiness check | When it fails |
|---|---|---|---|
| PostgreSQL | always | `database` (ping only) | `/ready` returns `503`; jobs cannot claim, renew or finish |
| Kubernetes API | live mode | `kubernetes` (`/version`) | `/ready` returns `503`; investigations whose target cannot be read fail |
| OAuth2 Proxy | `AUTH_ENABLED=true` | `authentication` | `/ready` and protected routes return `503` |
| OpenAI or Ollama | only when selected | none | analysis fails, evidence is kept, `502` on the waiting endpoint |
| metrics-server, Secret metadata RBAC, node RBAC | optional | none | that diagnostic is recorded as failed, the investigation continues |

`/ready` only pings PostgreSQL, so it can stay ready while writes fail, for
example when the database disk is full.

## Upgrade

Startup applies pending migrations under a PostgreSQL advisory lock: replicas
starting together apply them once. Do not run old and new versions at the same
time across a schema change (see [durable jobs](durable-jobs.md#upgrade-and-rollback)
and [report v2](analysis-contract.md#upgrade-and-rollback)). The supplied
Deployment uses the default rolling update, which would mix versions, so stop
it first:

```bash
kubectl -n troubleshooter scale deployment/troubleshooter --replicas=0
kubectl -n troubleshooter get pods -l app=troubleshooter   # repeat until none are listed
# back up PostgreSQL (next section), then apply the new release manifests:
kubectl apply -f kubernetes.yaml        # restores 2 replicas with the new image
kubectl -n troubleshooter rollout status deployment/troubleshooter --timeout=180s
```

Jobs interrupted by the stop recover or end failed as described in
[limits and recovery](durable-jobs.md#limits-and-recovery). To avoid that, wait
until `jobs_active` and `jobs_queue_depth` are `0` before scaling down.

## Backup and restore

Use PostgreSQL's own tools with a client version at least as new as the server:

```bash
pg_dump --format=custom --file=troubleshooter.dump "$DATABASE_URL"
createdb troubleshooter_restored            # an empty database
pg_restore --exit-on-error --no-owner -d troubleshooter_restored troubleshooter.dump
```

Point `DATABASE_URL` at the restored database and start the same application
version. It applies no migrations when the schema is current; incidents,
attempts, evidence and legacy and v2 reports read back unchanged.

Backups contain incident text and diagnostic output. Rows written before masking
was added are not rewritten, so treat backups as confidential. Deleting incidents
with `api purge` does not remove them from existing backups; restoring an older
backup brings deleted incidents back and loses anything written after it.

## Rollback

Startup, `api migrate up` and `api migrate down` refuse a database schema newer
than the binary's migrations and change nothing. Versions released before this
check do not have it. After a schema change, the safe rollback is to stop every
replica, restore the backup taken before the upgrade, and start the previous
version. This loses data written since the backup. For the documented reversible
cases, run `api migrate down` with the **newer** binary first; it refuses when
active jobs or v2 reports exist.

## Runbook

| Symptom | Check | Action |
|---|---|---|
| `/ready` `503`, `checks.database: unreachable` | PostgreSQL reachability and credentials | Restore the connection. Running jobs stop; when the database returns, recovery retries them or ends them failed, including any past `JOB_TIMEOUT` |
| Job routes return `503 unavailable`, other writes `500` | PostgreSQL logs, free disk | Free space or fix the database; readiness may still be green |
| `429 busy` on submission | `jobs_queue_depth` vs `JOB_QUEUE_CAPACITY`, `jobs_active` | Wait for `Retry-After`, add capacity, or raise the limit consistently on every replica |
| Job stays `running` | `stage` and `deadline_at` on the job endpoint, worker logs | A lease expires after 30s; another worker retries before analysis or ends it failed. Nothing changes while no worker runs |
| Job failed after stage `analyzing` | `llm_failures_total`, provider status and billing | Not retried automatically; the outcome can be unknown. Submit with a new `Idempotency-Key` |
| Diagnostic `get_resource_usage`, `get_secret_metadata` or `get_nodes` failed | metrics-server, optional RBAC, the `troubleshooter-node-reader` binding | Expected without them; install them if you need that evidence |
| Legacy `running` attempt without `job_id` | investigation history | Written before durable jobs; it is never repaired, and `api purge` skips its incident |

## Verification

`make test-upgrade` builds commit `06db1ed` (the last schema-1 version, not a
published release), writes a legacy report, starts two current replicas against
that database at once and checks that exactly one applies the migrations. It then
writes a v2 report, runs `pg_dump` and `pg_restore` into a new database on
PostgreSQL 17 and requires identical API reads before and after. CI runs it in
the `integration` job. Requires Docker, Go, curl, jq and Git.
