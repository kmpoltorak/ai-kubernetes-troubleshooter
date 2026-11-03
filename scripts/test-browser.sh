#!/usr/bin/env bash
# Runs the Playwright suite against the real binary with the embedded UI: one
# server with the rules analyzer and one whose OpenAI-compatible provider fails
# after a delay, each on its own database in a disposable PostgreSQL container.
set -Eeuo pipefail
umask 077

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
port=${BROWSER_PORT_BASE:-18280}
for tool in docker go node npx curl; do command -v "$tool" >/dev/null; done
[ -d web/node_modules ] || { echo 'run: cd web && npm ci && npx playwright install chromium' >&2; exit 2; }

# shellcheck source=scripts/harness.sh
source "$repo/scripts/harness.sh" browser

start() { # port database VAR=value...
  local p=$1 d=$2
  shift 2
  env -i PATH="$PATH" DATABASE_URL="postgres://akt:akt@127.0.0.1:$pg_port/$d?sslmode=disable" HTTP_PORT="$p" \
    SIMULATION_ENABLED=true AUTH_ENABLED=false "$@" "$work/api" > "$work/app-$p.log" 2>&1 &
  pids="$pids $!"
}

pg_port=$(pg_start)
docker exec "$db" createdb -U akt failing

(cd web && npm run build)
CGO_ENABLED=0 go build -o "$work/api" ./cmd/api

# Fails every chat completion after 8s, long enough to reload during analysis.
node -e 'require("http").createServer((req, res) => { req.resume(); setTimeout(() => {
  res.writeHead(500, { "Content-Type": "application/json" });
  res.end(JSON.stringify({ error: { message: "browser test provider failure" } }));
}, 8000); }).listen(Number(process.argv[1]), "127.0.0.1");' "$((port + 2))" > "$work/provider.log" 2>&1 &
pids="$pids $!"

# A low limit makes the UI's normal request bursts hit 429 rate_limited on every run.
start "$port" akt LLM_PROVIDER=rules RATE_LIMIT_RPS=2
start $((port + 1)) failing LLM_PROVIDER=openai OPENAI_API_KEY=browser-test OPENAI_BASE_URL="http://127.0.0.1:$((port + 2))/v1"
ready "$port"
ready $((port + 1))

(cd web && BROWSER_RULES_URL="http://127.0.0.1:$port" BROWSER_FAILING_URL="http://127.0.0.1:$((port + 1))" npx playwright test "$@")
