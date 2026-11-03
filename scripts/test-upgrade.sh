#!/usr/bin/env bash
# Upgrades a schema-1 database written by a baseline commit, starts two current
# replicas at once against it, backs it up with pg_dump, restores it into a new
# database and requires identical API reads before and after the restore.
set -Eeuo pipefail
umask 077

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
# Merge of PR #24: the last commit before migration 0002. Not a published release.
baseline=${UPGRADE_BASELINE:-06db1ed6668f6af45e20c69bbc55be1218935985}
port=${UPGRADE_PORT_BASE:-18180}
for tool in docker go curl jq git tar; do command -v "$tool" >/dev/null; done

# shellcheck source=scripts/harness.sh
source "$repo/scripts/harness.sh" upgrade

sql() { docker exec "$db" psql -U akt -d "$1" -v ON_ERROR_STOP=1 -Atc "$2"; }
api() { curl -fsS --max-time 120 -H 'Content-Type: application/json' "$@"; }

start() { # binary port database
  env -i DATABASE_URL="postgres://akt:akt@127.0.0.1:$pg_port/$3?sslmode=disable" HTTP_PORT="$2" \
    SIMULATION_ENABLED=true LLM_PROVIDER=rules AUTH_ENABLED=false "$1" > "$work/app-$2.log" 2>&1 &
  pids="$pids $!"
}

# SIGTERM is the normal shutdown; a non-zero exit fails the test.
stop_all() {
  for pid in $pids; do kill "$pid"; wait "$pid"; done
  pids=
}

applied() { grep -o '"migrations_applied":[0-9]*' "$work/app-$1.log" | cut -d: -f2; }

snapshot() { # port dir
  local base="http://127.0.0.1:$1/api/v1" id inv
  mkdir "$2"
  api "$base/incidents" > "$2/incidents.json"
  for id in "$old_id" "$new_id"; do
    api "$base/incidents/$id" > "$2/$id.json"
    api "$base/incidents/$id/report" > "$2/$id-report.json"
    api "$base/incidents/$id/investigations" > "$2/$id-investigations.json"
    for inv in $(jq -r '.investigations[].id' "$2/$id-investigations.json"); do
      api "$base/incidents/$id/investigations/$inv" > "$2/$inv.json"
    done
  done
}

pg_port=$(pg_start)

if ! git cat-file -e "$baseline^{commit}" 2>/dev/null; then git fetch -q --depth 1 origin "$baseline"; fi
mkdir "$work/baseline"
git archive "$baseline" | tar -x -C "$work/baseline"
(cd "$work/baseline" && CGO_ENABLED=0 go build -o "$work/api-old" ./cmd/api)
CGO_ENABLED=0 go build -o "$work/api-new" ./cmd/api

echo "baseline $baseline writes a legacy report"
start "$work/api-old" "$port" akt
ready "$port"
[ "$(sql akt 'SELECT max(version) FROM schema_migrations')" = 1 ]
old_id=$(api -X POST "http://127.0.0.1:$port/api/v1/incidents" -d @docs/examples/oom_killed.json | jq -er .id)
api -X POST "http://127.0.0.1:$port/api/v1/incidents/$old_id/investigate" -d '{"scenario":"oom_killed"}' \
  | jq -e '.report.analysis.confidence > 0 and (.report.analysis.schema_version == null)' >/dev/null
stop_all

echo "two current replicas start against the baseline schema"
versions=$(for f in migrations/*.up.sql; do f=${f#migrations/}; echo $((10#${f%%_*})); done | sort -n)
start "$work/api-new" $((port + 1)) akt
start "$work/api-new" $((port + 2)) akt
ready $((port + 1))
ready $((port + 2))
[ "$(sql akt 'SELECT version FROM schema_migrations ORDER BY version')" = "$versions" ]
# The advisory lock lets exactly one replica apply the pending migrations.
[ "$(printf '%s\n' "$(applied $((port + 1)))" "$(applied $((port + 2)))" | sort -n | paste -sd, -)" = "0,$(($(wc -l <<< "$versions") - 1))" ]
api "http://127.0.0.1:$((port + 2))/api/v1/incidents/$old_id/report" \
  | jq -e '.report.analysis.confidence > 0' >/dev/null
new_id=$(api -X POST "http://127.0.0.1:$((port + 1))/api/v1/incidents" -d @docs/examples/crash_loop_backoff.json | jq -er .id)
api -X POST "http://127.0.0.1:$((port + 2))/api/v1/incidents/$new_id/investigate" -d '{"scenario":"crash_loop_backoff"}' \
  | jq -e '.report.analysis.schema_version == 2' >/dev/null
snapshot $((port + 1)) "$work/before"
stop_all

echo "pg_dump, pg_restore into a new database, compare API reads"
docker exec "$db" pg_dump -U akt -d akt --format=custom --file=/tmp/akt.dump
docker exec "$db" createdb -U akt akt_restored
docker exec "$db" pg_restore -U akt -d akt_restored --exit-on-error --no-owner /tmp/akt.dump
start "$work/api-new" $((port + 3)) akt_restored
ready $((port + 3))
[ "$(applied $((port + 3)))" = 0 ]
snapshot $((port + 3)) "$work/after"
diff -r "$work/before" "$work/after"
stop_all
echo "upgrade, two-replica migration and restore: PASS"
