# Getting started

This guide takes you from `git clone` to a running installation, one step at a
time. Steps 1–3 give you a working app in simulation mode in a few minutes.
Steps 4–6 connect your own database, a real cluster and a language model;
each one is optional and builds on the previous steps.

## 1. Install the prerequisites

| You need | For | Check with |
|---|---|---|
| [Docker](https://docs.docker.com/get-started/get-docker/) with the Compose plugin | every step | `docker compose version` |
| Git | cloning | `git --version` |
| `kubectl` with admin access to the cluster | step 5 only | `kubectl get nodes` |

The app runs in Docker, so you do not need Go or Node.js.

## 2. Get the code

```bash
git clone https://github.com/kmpoltorak/ai-kubernetes-troubleshooter.git
cd ai-kubernetes-troubleshooter
cp .env.example .env
```

All settings live in `.env`. Docker Compose reads it automatically. Every
variable is explained in `.env.example` and in the
[configuration reference](configuration.md).

## 3. Try it in simulation

Simulation runs investigations against built-in fake clusters, so no cluster,
database or API key is needed:

```bash
docker compose -f docker-compose.yml -f docker-compose.test-db.yml up -d --build
```

The first build takes a few minutes. Then:

```bash
curl -s localhost:8080/ready      # {"status":"ready", ... "database":"ok"}
```

Open <http://localhost:8080>:

1. Pick a sample incident, for example **OOM**. It fills in the form and
   chooses a scenario.
2. Click **Create incident**, then **Run investigation**.
3. The report shows the verdict, the cited evidence and read-only `kubectl`
   commands. Each "?" next to a term explains it.

`docker-compose.test-db.yml` adds a throwaway PostgreSQL for this trial. It
publishes no port, so it cannot clash with a database already on your machine.
Do not keep real data in it.

If port 8080 is taken, set `HTTP_PORT=18080` in `.env`, run the command again
and use that port in the URLs.

Stop it, and delete the throwaway database:

```bash
docker compose -f docker-compose.yml -f docker-compose.test-db.yml down -v
```

## 4. Use your own PostgreSQL

The app needs PostgreSQL. It creates its tables when it starts. Point
`DATABASE_URL` in `.env` at your database:

```
DATABASE_URL=postgres://troubleshooter:PASSWORD@db.example.net:5432/troubleshooter?sslmode=require
```

Inside the container, `localhost` is the container itself, not your machine.
Pick the address the container can reach:

| Your database runs | `DATABASE_URL` host | Also needed |
|---|---|---|
| on another server | its hostname or IP | nothing |
| in a container on the same host, for example from another Compose project | the container name | `EXTERNAL_DATABASE_NETWORK=<its network>` in `.env` and `-f docker-compose.external-db.yml` when starting |

`docker inspect <database container>` lists the container's network under
`Networks`. Then start without the test database:

```bash
docker compose up -d --build
# with a database container on the same host:
docker compose -f docker-compose.yml -f docker-compose.external-db.yml up -d --build
```

## 5. Connect a real cluster

The app reads the cluster with a ServiceAccount that can only `get` and `list`:
no writes, no `exec`, no Secret values. You set that account up once, then give
the app a kubeconfig with its token.

**5.1 Create the account.** Use your admin `kubectl`. The supplied RBAC grants
read access in the `payments` namespace:

```bash
kubectl create namespace payments --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deployments/kubernetes/namespace.yaml \
  -f deployments/kubernetes/serviceaccount.yaml -f deployments/kubernetes/rbac.yaml
```

To read other namespaces, copy the `RoleBinding` at the end of
[rbac.yaml](../deployments/kubernetes/rbac.yaml) once for each namespace and
change its `namespace`.

**5.2 Write a kubeconfig for the account.** The token below is valid for 30
days. Your cluster may allow less. The file holds only this account's
credentials, not your admin ones:

```bash
KC=$HOME/.kube/troubleshooter.yaml
CLUSTER=$(kubectl config view --minify -o jsonpath='{.clusters[0].name}')
SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
kubectl config view --minify --flatten -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > /tmp/troubleshooter-ca.crt
TOKEN=$(kubectl -n troubleshooter create token troubleshooter --duration=720h)

kubectl config --kubeconfig="$KC" set-cluster "$CLUSTER" --server="$SERVER" \
  --certificate-authority=/tmp/troubleshooter-ca.crt --embed-certs=true
kubectl config --kubeconfig="$KC" set-credentials troubleshooter --token="$TOKEN"
kubectl config --kubeconfig="$KC" set-context troubleshooter --cluster="$CLUSTER" --user=troubleshooter
kubectl config --kubeconfig="$KC" use-context troubleshooter
rm /tmp/troubleshooter-ca.crt

kubectl --kubeconfig="$KC" -n payments get deployments   # must work
kubectl --kubeconfig="$KC" -n payments delete pod x      # must be Forbidden
```

The app runs as user 65532 inside the container. On Linux, let that user read
the file while it stays private to you:

```bash
sudo chgrp 65532 "$KC" && chmod 0640 "$KC"
```

**5.3 Tell the app.** Add to `.env`. Use the full path: `make` does not expand
`$HOME` in `.env`.

```
KUBECONFIG_FILE=/home/you/.kube/troubleshooter.yaml
KUBERNETES_ALLOWED_NAMESPACES=payments
```

The cluster's name in the app comes from the kubeconfig, so it is `$CLUSTER`
from step 5.2. To choose another, set `KUBERNETES_CLUSTER_NAME`. You must set it
when that name is not a lowercase DNS name, for example an EKS ARN.

**5.4 Start in live mode.** Add `-f docker-compose.live.yml`. Keep
`-f docker-compose.external-db.yml` if you used it in step 4.

```bash
docker compose -f docker-compose.yml -f docker-compose.live.yml up -d --build
```

The UI header shows `mode: live` and `1 cluster reachable`. The form now lists
the namespaces and suggests object names from the cluster.

The server address in the kubeconfig must be reachable from a container.
`https://127.0.0.1:…` or `localhost`, as used by kind, minikube or Docker
Desktop, points at the container itself, so the cluster shows as
`unreachable`. Several clusters at once: see [several clusters](clusters.md).

## 6. Add a language model (optional)

Without one, the built-in `rules` analyzer writes the reports. It works offline
and sends nothing out. To use a model, add to `.env`:

```
LLM_PROVIDER=openai          # or anthropic, gemini
LLM_MODEL=gpt-6-luna         # an exact model name your key can use
OPENAI_API_KEY=...           # ANTHROPIC_API_KEY or GEMINI_API_KEY for the others
```

Then run your start command again with `--force-recreate app` at the end, for
example:

```bash
docker compose -f docker-compose.yml -f docker-compose.live.yml up -d --force-recreate app
```

At startup the app checks the model against the provider's list for your key.
If the model is not in the list, the app stops and prints the models it can
use. With a model, masked incident and diagnostic data goes to that provider.
A model can be wrong: the report says so and cites the evidence to check.
**Settings** in the header shows what is configured.

## Day-to-day

| Task | Command |
|---|---|
| Update to the latest version | `git pull`, then start with `--build` |
| Apply `.env` or kubeconfig changes | the start command plus `--force-recreate app`; the kubeconfig is read only at start |
| Renew the cluster token | step 5.2, then `--force-recreate app` |
| Logs | `docker compose logs app` |
| Stop | `docker compose down` (with the same `-f` files) |

## When something goes wrong

The app checks its configuration at startup and lists every problem at once.
Read `docker compose logs app` first.

| You see | Cause | Fix |
|---|---|---|
| `DATABASE_URL is required` | no database configured | step 4, or add `-f docker-compose.test-db.yml` |
| `port is already allocated` | another program uses the port | `HTTP_PORT=18080` in `.env` |
| `connect to database … no such host` | the database container is on another Docker network | `EXTERNAL_DATABASE_NETWORK` and `-f docker-compose.external-db.yml` (step 4) |
| `required variable KUBECONFIG_FILE is missing` | live mode without a kubeconfig | step 5.3 |
| `permission denied` for `/kube/config` | the container cannot read the kubeconfig | the `chgrp`/`chmod` in step 5.2 |
| header: `credentials rejected` | the token expired or is invalid | renew it (step 5.2) and recreate the app |
| header: `unreachable` | the API server address cannot be reached from the container | see the note at the end of step 5 |
| `LLM_MODEL "…" is not in …'s list` | wrong or unavailable model name | pick one from the printed list |
| `the provider rejected the API key` | wrong or revoked key | fix the key in `.env` |

More: [operations runbook](operations.md#runbook), [limitations](limitations.md).
