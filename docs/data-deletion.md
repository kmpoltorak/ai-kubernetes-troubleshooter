# Delete stored incident data

Signed-in users delete chosen incidents from the UI or with
`POST /api/v1/incidents/delete` (see the [API reference](api.md)). Any user admitted
by authentication may delete; there is no undo, and the server log records the
signed-in user and the deleted IDs. This page covers the operator command that
removes old incidents by date.

`api purge` is an operator command using `DATABASE_URL`. It operates across the
whole database, independent of the server's cluster and namespace allowlist.
It needs a database account allowed to lock and delete these records; browser
login does not grant this access. It does not start the server, contact Kubernetes
or an LLM, or require SSO settings. Apply the application's database migrations before using it.

There is no automatic retention job. Without `--confirm`, the command only
prints a preview and releases its temporary database locks.

## Preview, inspect, confirm

Build the binary with `make build`. Set `DATABASE_URL` through your environment
or secret manager. Choose a cutoff, then preview:

```bash
./bin/api purge --before 2026-01-01T00:00:00Z --limit 100
```

The JSON output contains `before`, `limit`, `candidates`, `confirmation` and
`deleted`. Each candidate shows its ID, creation/update times and counts of
jobs, investigations, tool executions, evidence, reports and remediation actions.
Incident descriptions and diagnostic contents are not printed. `deleted` is
`false` for a preview.

Review the IDs and counts. To permanently delete that batch, copy the
`confirmation` value from the preview and pass it with the same cutoff and limit:

```bash
./bin/api purge --before 2026-01-01T00:00:00Z --limit 100 --confirm "$CONFIRMATION"
```

Success returns the same selection with `deleted: true`. If the set or counts
changed, or the cutoff/limit differs, the command rejects the token and deletes
nothing. Run a new preview and review it. Confirming an empty selection changes
no rows.

For a running Compose installation, use its app container and database settings:

```bash
docker compose exec -T app /api purge --before 2026-01-01T00:00:00Z --limit 100
docker compose exec -T app /api purge --before 2026-01-01T00:00:00Z --limit 100 --confirm "$CONFIRMATION"
```

| Flag | Meaning |
|---|---|
| `--before` | required RFC3339 timestamp with a timezone; exclusive, cannot be in the future |
| `--limit` | incident count per batch; default 100, range 1–1000 |
| `--confirm` | token returned by the matching preview; omitting it previews only |
| `--help` | usage without a database connection |

## Which records are eligible

Both the incident's creation and update times must be strictly before the
cutoff. Every investigation must have finished before that cutoff and started
before it. Queued/running investigations and investigations without a completion time
exclude the entire incident. This also preserves old incidents that were
recently reinvestigated. Every job must also be terminal, created before the
cutoff and completed before it. Waiting jobs, including retries, protect the
entire incident. An incident with no investigations or jobs can be deleted.

Old completed and failed investigations are removed together with their incident,
jobs (including their idempotency keys), tool executions, evidence, reports and remediation actions. The batch is one
transaction: a database error or cancellation before commit rolls it back.
The command has a 30-second deadline and database statements have a 10-second
timeout. Row locks protect concurrent investigation creation; locked incidents
are skipped. Concurrent activity can invalidate a preview.

If the process loses its connection or output around commit, inspect the database
with a new preview before retrying. An output error after a known commit says
`purge committed`; it cannot undo the deletion. Repeat the preview/confirm cycle
for further batches. Do not wrap it in an unattended deletion loop.

## Backups and remaining copies

Deletion is permanent in the active database. It does not erase PostgreSQL
backups, WAL archives, replicas' historical copies, exported files or previously
sent LLM requests. It is not secure erasure of disk pages. Manage those copies
under your own backup and provider policies. Restoring an older backup can
restore deleted incidents.

Interrupted investigations that remain `running` are retained even when old;
this command does not repair their state. It also does not rewrite existing
records to apply redaction. API reads continue masking historical data.
