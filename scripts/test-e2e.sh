#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
version=${E2E_KUBERNETES_VERSION:-1.37.0}
case "$version" in
  1.37.0) node_image=kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5 ;;
  1.36.4) node_image=kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed ;;
  *) echo 'E2E_KUBERNETES_VERSION must be 1.36.4 or 1.37.0' >&2; exit 2 ;;
esac
for tool in docker kind kubectl go; do command -v "$tool" >/dev/null; done
case "$(kind version)" in 'kind v0.33.0 '*) ;; *) echo 'kind v0.33.0 is required' >&2; exit 2 ;; esac

work=$(mktemp -d "${TMPDIR:-/tmp}/akt-e2e.XXXXXXXX")
suffix=$(basename "$work" | tr '[:upper:]' '[:lower:]' | tr -d '.')
cluster="akt-e2e-${suffix}"
context="kind-${cluster}"
artifacts=${E2E_ARTIFACT_DIR:-"$repo/report-e2e-${version}-${suffix}"}
owned=0
test_pid=
port_pid=
export KUBECONFIG="$work/kubeconfig" E2E_KUBECONFIG="$work/kubeconfig" E2E_CONTEXT="$context" E2E_CLUSTER_NAME="$cluster"
export E2E_FIXTURE_IMAGE="akt-e2e-fixture:${suffix}" E2E_REPO="$repo" E2E_ARTIFACT_DIR="$artifacts"

k() { kubectl --kubeconfig "$work/kubeconfig" --context "$context" --request-timeout=10s "$@"; }
cleanup() {
  status=$?
  trap - EXIT INT TERM
  for pid in "$test_pid" "$port_pid"; do
    if [ -n "$pid" ]; then
      if kill -0 "$pid" 2>/dev/null; then kill "$pid"; fi
      # These children normally report SIGTERM when the runner stops them.
      if wait "$pid"; then :; else child_status=$?; if [ "$child_status" -ne 143 ] && [ "$child_status" -ne 130 ]; then echo "child exit: $child_status" >&2; fi; fi
    fi
  done
  if [ "$status" -ne 0 ] && [ -s "$work/kubeconfig" ]; then
    if [ -x "$work/e2e.test" ]; then
      if "$work/e2e.test" -test.v -test.run '^TestCollectDiagnostics$' -test.timeout=60s > "$artifacts/diagnostics.log" 2>&1; then :; else echo 'diagnostic collection failed' >&2; fi
    else
      if k get pods -A -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase > "$artifacts/pods.txt" 2>&1; then :; else echo 'pod status collection failed' >&2; fi
    fi
  fi
  if [ "$owned" -eq 1 ]; then
    if kind delete cluster --name "$cluster" --kubeconfig "$work/kubeconfig"; then :; else echo 'owned cluster cleanup failed' >&2; if [ "$status" -eq 0 ]; then status=1; fi; fi
  fi
  rm -rf "$work"
  echo "Live-test output: $artifacts"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

case "$(basename "$artifacts")" in report-*) ;; *) echo 'artifact directory name must start with report-' >&2; exit 2 ;; esac
mkdir "$artifacts"

existing_clusters=$(kind get clusters)
if printf '%s\n' "$existing_clusters" | grep -Fxq "$cluster"; then echo 'cluster name already exists' >&2; exit 2; fi
api_image="akt-e2e-api:${suffix}"
export E2E_BUILD_VERSION E2E_BUILD_REVISION E2E_BUILD_TIME
if [ -n "${E2E_IMAGE_ARTIFACT:-}" ]; then
  bash scripts/image-artifact.sh verify "$E2E_IMAGE_ARTIFACT"
  api_image="ghcr.io/kmpoltorak/ai-kubernetes-troubleshooter@$(jq -r .manifest_digest "$E2E_IMAGE_ARTIFACT/metadata.json")"
  E2E_BUILD_VERSION=$(jq -r .version "$E2E_IMAGE_ARTIFACT/metadata.json")
  E2E_BUILD_REVISION=$(jq -r .revision "$E2E_IMAGE_ARTIFACT/metadata.json")
  E2E_BUILD_TIME=$(jq -r .built_at "$E2E_IMAGE_ARTIFACT/metadata.json")
else
  E2E_BUILD_REVISION=$(git rev-parse HEAD)
  E2E_BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  E2E_BUILD_VERSION=e2e
  echo "Building application and fixture for Kubernetes $version"
  docker build --build-arg "BUILD_VERSION=$E2E_BUILD_VERSION" --build-arg "BUILD_REVISION=$E2E_BUILD_REVISION" --build-arg "BUILD_TIME=$E2E_BUILD_TIME" --tag "$api_image" .
  for field in version revision created; do
    case "$field" in version) expected=$E2E_BUILD_VERSION ;; revision) expected=$E2E_BUILD_REVISION ;; created) expected=$E2E_BUILD_TIME ;; esac
    actual=$(docker image inspect --format "{{index .Config.Labels \"org.opencontainers.image.$field\"}}" "$api_image")
    if [ "$actual" != "$expected" ]; then echo "image build label mismatch: $field" >&2; exit 1; fi
  done
fi
docker build --file tests/e2e/fixture/Dockerfile --tag "$E2E_FIXTURE_IMAGE" .
go test -c -tags e2e -o "$work/e2e.test" ./tests/e2e
owned=1
kind create cluster --name "$cluster" --kubeconfig "$work/kubeconfig" --image "$node_image" --wait 5m
if [ -n "${E2E_IMAGE_ARTIFACT:-}" ]; then
  kind load image-archive --name "$cluster" "$E2E_IMAGE_ARTIFACT/image.oci.tar"
  docker exec "$cluster-control-plane" ctr --namespace k8s.io images tag \
    "$(jq -r .config_digest "$E2E_IMAGE_ARTIFACT/metadata.json")" "$api_image"
  docker exec "$cluster-control-plane" crictl inspecti "$api_image" > "$work/imported-image.json"
  test "$(jq -r .status.id "$work/imported-image.json")" = "$(jq -r .config_digest "$E2E_IMAGE_ARTIFACT/metadata.json")"
  kind load docker-image --name "$cluster" "$E2E_FIXTURE_IMAGE"
else
  kind load docker-image --name "$cluster" "$api_image" "$E2E_FIXTURE_IMAGE"
fi

cp -R deployments/kubernetes "$work/base"
cp tests/e2e/postgres.yaml "$work/postgres.yaml"
password=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
printf 'POSTGRES_PASSWORD=%s\nDATABASE_URL=postgres://troubleshooter:%s@postgres:5432/troubleshooter?sslmode=disable\n' "$password" "$password" > "$work/database.env"
unset password
cat > "$work/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - base
  - postgres.yaml
secretGenerator:
  - name: troubleshooter-secrets
    namespace: troubleshooter
    envs: [database.env]
generatorOptions:
  disableNameSuffixHash: true
patches:
  - target:
      kind: Deployment
      name: troubleshooter
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/image
        value: $api_image
      - op: add
        path: /spec/template/spec/containers/0/imagePullPolicy
        value: Never
EOF
k create namespace payments
k apply -k "$work" > "$artifacts/apply.txt"
k -n troubleshooter rollout status deployment/postgres --timeout=180s
k -n troubleshooter rollout status deployment/troubleshooter --timeout=180s
k -n troubleshooter port-forward --address 127.0.0.1 service/troubleshooter :80 > "$work/port-forward.log" 2>&1 &
port_pid=$!
address=
for ((attempt=0; attempt<50; attempt++)); do
  address=$(awk '/Forwarding from 127.0.0.1:/ {print $3; exit}' "$work/port-forward.log")
  if [ -n "$address" ]; then break; fi
  if ! kill -0 "$port_pid" 2>/dev/null; then echo 'port-forward exited' >&2; exit 1; fi
  sleep 0.2
done
if [ -z "$address" ]; then echo 'port-forward did not start' >&2; exit 1; fi
export E2E_API_URL="http://${address}" E2E_KUBERNETES_VERSION="$version"
echo "Running live cases on $context"
"$work/e2e.test" -test.v -test.run '^TestLiveCluster$' -test.timeout=30m > "$artifacts/tests.log" 2>&1 &
test_pid=$!
if wait "$test_pid"; then test_pid=; else result=$?; test_pid=; if cat "$artifacts/tests.log"; then :; else echo "test log unavailable" >&2; fi; exit "$result"; fi
cat "$artifacts/tests.log"
