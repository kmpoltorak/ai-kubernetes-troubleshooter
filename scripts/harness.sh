# shellcheck shell=bash
# Sourced by test-upgrade.sh and test-browser.sh with a short name: a work
# directory, a disposable PostgreSQL container, background servers tracked in
# $pids, and cleanup (with server logs on failure) on every exit.
work=$(mktemp -d "${TMPDIR:-/tmp}/akt-$1.XXXXXXXX")
db="akt-$1-$(basename "$work" | tr '[:upper:]' '[:lower:]' | tr -d '.')"
pids=
cleanup() {
  status=$?
  trap - EXIT
  for pid in $pids; do { kill "$pid" && wait "$pid"; } 2>/dev/null || :; done
  if [ "$status" -ne 0 ]; then
    for log in "$work"/*.log; do [ -f "$log" ] && { echo "== $log" >&2; tail -n 20 "$log" >&2; }; done
  fi
  docker rm -f "$db" >/dev/null 2>&1 || :
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'echo "$(basename "$0"): failed at line $LINENO" >&2' ERR
trap 'exit 130' INT
trap 'exit 143' TERM

ready() {
  for _ in $(seq 60); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$1/ready" >/dev/null 2>&1; then return; fi
    sleep 1
  done
  echo "server on port $1 did not become ready" >&2
  return 1
}

# Prints the published port. TCP readiness: the image's temporary init server listens on the socket only.
pg_start() {
  docker run -d --rm --name "$db" -e POSTGRES_USER=akt -e POSTGRES_PASSWORD=akt -e POSTGRES_DB=akt \
    -p 127.0.0.1::5432 postgres:17-alpine >/dev/null
  for _ in $(seq 60); do
    if docker exec "$db" pg_isready -h 127.0.0.1 -U akt -d akt >/dev/null 2>&1; then break; fi
    sleep 1
  done
  docker exec "$db" pg_isready -h 127.0.0.1 -U akt -d akt >/dev/null
  docker port "$db" 5432/tcp | head -n 1 | sed 's/.*://'
}
