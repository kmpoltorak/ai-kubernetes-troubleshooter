# Live Kubernetes tests

The CI `kubernetes` matrix runs the built application against a real API server,
kubelet, controllers and NetworkPolicy enforcement. It uses one kind node per
job on Linux amd64. The seven other CI jobs remain enabled.

| Kubernetes | kind | Node image SHA256 |
|---|---|---|
| 1.36.4 | 0.33.0 | `099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed` |
| 1.37.0 | 0.33.0 | `a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5` |

The images are from the [kind 0.33.0 release](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0).
CI verifies downloaded kind and kubectl binaries against their release checksums.
It uses kubectl 1.37.0; local kubectl must be compatible with the chosen server.

## Run

Requires Docker running Linux containers, Go from `go.mod`, kind 0.33.0 and
kubectl on `PATH`. Follow [kind installation](https://kind.sigs.k8s.io/docs/user/quick-start/)
and verify the release checksum before running a downloaded binary. Allow space
and memory for a control plane, PostgreSQL, two API replicas and fixture pods.

```bash
make test-e2e                                     # Kubernetes 1.37.0
E2E_KUBERNETES_VERSION=1.36.4 make test-e2e
```

Each command builds the application Dockerfile and a small test fixture, compiles
the Go tests, creates a uniquely named `akt-e2e-*` kind cluster, and uses its own
temporary kubeconfig and explicit context. It never selects your current context
or reads `.env`. The test code also requires the expected single-node kind name
and a loopback API endpoint before writing fixtures. It cannot target a remote
production cluster.

The runner applies a temporary copy of the base Kustomize manifests, retains two
replicas and the base ServiceAccount, loads the local application image, and adds
a disposable PostgreSQL 17 with generated credentials. It uses live diagnostics,
the rules analyzer and `AUTH_ENABLED=false`. HTTP access is through a loopback
port-forward. Fixture creation and fault injection use a separate administrator
client; the application receives no extra write permissions.

## Checks

Every investigation uses POST incident → POST durable job → terminal status →
saved history/report. Completed reports must pass v2 validation with citations
from their own attempt. Failed attempts must preserve evidence without inventing
a report. Tests wait for actual pod/controller conditions; they do not write
pod statuses or use simulated cluster responses.

| Case | Required observation |
|---|---|
| healthy Deployment, Pod and Service | ready pod/controller and EndpointSlice; bounded no-fault assessment |
| missing ConfigMap | CreateContainerConfigError, missing-reference evidence, disclosed failed log read |
| failed readiness probe | Running but not ready, actual probe warning event and corresponding evidence |
| invalid image | InvalidImageName and image-error evidence, without relying on a registry outage |
| Service selector mismatch | no matching pods and selector-mismatch evidence |
| namespace policy | API rejection outside allowlist; actual workload read by the SA is Forbidden |
| read-only RBAC | actual pod create/patch/delete and Secret reads forbidden; access reviews also reject exec/attach/portforward |
| denied log read | retained failure, missing-data disclosure and inconclusive assessment |
| unreadable target | terminal error, preserved evidence, no fabricated diagnosis |
| absent metrics-server | failed optional diagnostic disclosed; API remains ready |
| optional Secret metadata grant | failure without role; expected metadata after applying the existing optional manifest, without values or annotations |
| NetworkPolicy | new TCP connections succeed, deny policy causes timeouts, additive allow restores connections |

The NetworkPolicy source fixture makes a fresh bounded TCP connection every two
seconds to a local pod IP, without DNS or an external service. The test requires
three new outcomes after each transition and a successful independent control
client to the same destination. Logs are collected through the kubelet; the
control channel does not depend on a response from the isolated source pod.
The application must still describe policy causality as a hypothesis: it only
sees declarations and log observations, not this test's traffic controls.

RBAC checks use a short-lived token for the actual ServiceAccount. Temporary
restrictions are applied to a copy of its ClusterRole rules and restored after
each subtest. The optional `get secrets` grant also permits value reads with
another client; Kubernetes RBAC cannot grant metadata-only access. The test
preserves that documented trade-off. Namespace object metadata remains globally
readable through the existing namespace-reader grant.

## Timeouts and output

Cluster startup is bounded to five minutes; component/fixture waits to three
minutes, API jobs to two minutes, and the test binary to thirty minutes. Each CI
job has a forty-five-minute limit. There is no retry of the whole suite.

Output goes to a new `report-e2e-*` directory printed by the runner. Override
`E2E_ARTIFACT_DIR` with a new directory whose basename starts with `report-`.
The runner retains the test log and apply status; failures add selected pod
statuses, events and bounded masked application/fixture logs. It never exports
Secret objects, kubeconfigs, environment dumps, tokens or database contents.
CI retains these artifacts for three days. The temporary kubeconfig, credentials
and owned cluster are removed on exit, failure or a handled interrupt. Docker
build cache and local image tags remain available for subsequent runs.

## Limits

This checks the exact two versions and kind's default policy implementation.
It does not establish support for other patch versions, ARM64, managed clouds,
other CNI implementations, external identity providers, TLS/Ingress, browser
interactions or real-model quality. The separate authentication CI job exercises
OAuth2 Proxy/OIDC without a Kubernetes deployment. An uncatchable process kill
or unavailable Docker daemon may prevent cleanup; remove only the printed owned
cluster after resolving the failure.
