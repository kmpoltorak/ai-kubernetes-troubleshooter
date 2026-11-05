# Live Kubernetes tests

Run diagnostics, RBAC and NetworkPolicy checks on a disposable single-node kind
cluster. CI covers Kubernetes **1.36.4** and **1.37.0** on pushes to `main` and
release candidates, and 1.37.0 on pull requests, Linux amd64, using one shared
OCI image for every job.

## Run

Requires Docker running Linux containers, Go from `go.mod`, kind **0.33.0** and
kubectl compatible with the selected Kubernetes version. Install kind from its
[official release](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0)
and verify the release checksum. CI uses kubectl 1.37.0 and verifies both tools.

```bash
make test-e2e                                     # Kubernetes 1.37.0
E2E_KUBERNETES_VERSION=1.36.4 make test-e2e
```

The runner builds local source and a fixture image, creates an `akt-e2e-*`
cluster and uses its own kubeconfig/context. It does not read `.env` or select
your current context. It deploys PostgreSQL 17 and two API replicas in live mode
with the rules analyzer and authentication disabled. Allow resources for these
containers plus the kind control plane and fixture workloads.

To check a downloaded [candidate artifact](releases.md) instead of rebuilding
the application, install Skopeo, jq and shasum, then run:

```bash
E2E_IMAGE_ARTIFACT=/path/to/candidate make test-e2e
```

The artifact checksum, image metadata and imported configuration ID must match.
The Deployment uses the original manifest digest. Only the fixture is rebuilt.

## Scope and output

Cases cover healthy targets (including a StatefulSet and its node), a failed
Job, an Ingress with a missing backend, a Pending claim, an autoscaler without
metrics, a custom resource reporting `Ready=False`, missing configuration, failed readiness, invalid images, Service selectors, denied reads, optional diagnostics and actual TCP
NetworkPolicy enforcement. Fixture changes use an administrator client; the
application keeps the base read-only ServiceAccount. One case temporarily adds
the existing optional Secret metadata grant, then restores it. That grant also
allows value reads with other clients: RBAC cannot grant metadata-only access.

Cluster startup is limited to five minutes, fixture waits to three minutes and
the test binary to thirty minutes. Output is written to a new `report-e2e-*`
directory; `E2E_ARTIFACT_DIR` can select another new directory starting with
`report-`. Failures include bounded, masked diagnostics rather than Secret
objects, kubeconfigs or database contents. CI retains output for three days.

The runner removes its cluster, credentials and temporary files after success,
failure or a handled interrupt. Docker image tags/cache remain. If Docker fails
or the process is killed without cleanup, remove only the printed owned cluster
once Docker is available again.

These checks do not establish support for other Kubernetes versions, ARM64,
managed clouds, other CNI implementations, external identity providers,
TLS/Ingress, browser interactions or real-model quality.
