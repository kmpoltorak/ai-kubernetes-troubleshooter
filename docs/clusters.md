# Several clusters

One installation can read several clusters. An incident's `cluster` names one of
them, and each cluster has its own credentials and its own allowed namespaces.
Without `KUBERNETES_CLUSTERS_FILE`, the single-cluster variables (`KUBECONFIG`,
`KUBERNETES_CONTEXT`, `KUBERNETES_CLUSTER_NAME`, `KUBERNETES_ALLOWED_NAMESPACES`)
configure one cluster, as before. With the file, only `KUBECONFIG` still applies:
to entries without their own `kubeconfig`.

## Clusters file

`KUBERNETES_CLUSTERS_FILE` points at a YAML file:

```yaml
clusters:
  - name: lab
    kubeconfig: /kube/lab.yaml
    allowed_namespaces: [payments]
  - name: prod
    kubeconfig: /kube/prod.yaml
    context: prod-readonly
    allowed_namespaces: [checkout, orders]
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | the value incidents use in `cluster`; lowercase DNS name, unique |
| `kubeconfig` | no | path inside the container; empty uses the in-cluster ServiceAccount in a pod, otherwise `$KUBECONFIG` or `~/.kube/config` |
| `context` | no | kubeconfig context; empty uses its current context |
| `allowed_namespaces` | no | allowlist for this cluster; empty allows every namespace the credentials can read |

The app refuses to start when the file is unreadable, has an unknown field (a
typo such as `allowed_namespace`), lists no cluster, repeats a name, has an
invalid name or namespace, or when `KUBERNETES_CLUSTER_NAME`, `KUBERNETES_CONTEXT`
or `KUBERNETES_ALLOWED_NAMESPACES` is also set. The API never returns paths or
contexts, and the configuration line in the startup log lists cluster names only.

Each cluster needs the read-only ServiceAccount and RoleBindings from
[`deployments/kubernetes/rbac.yaml`](../deployments/kubernetes/rbac.yaml), and a
kubeconfig for that account. `KUBERNETES_CUSTOM_RESOURCES`, the analyzer and the
limits apply to every cluster.

## Status

The UI header shows how many clusters answer, and names the ones that do not.
The form marks unreachable clusters; they can still be chosen. The UI repeats the check every
30 seconds through `GET /api/v1/clusters`, which returns `ok` or
`unreachable` per cluster (`simulated` in simulation). The connection errors go
to the log, because they contain API server addresses.

`/ready` reports one `kubernetes` check, without cluster names because it is
public. It passes as soon as one cluster answers, so an unreachable or hanging
cluster does not take the installation out of service.

## Docker Compose

Put `clusters.yaml` and the kubeconfigs it names in one directory, readable by
the container user (uid 65532). Inside the container the directory is `/kube`:

```bash
KUBE_DIR=$HOME/troubleshooter-clusters \
  docker compose -f docker-compose.yml -f docker-compose.clusters.yml up --build
```

## Kubernetes

Mount the file from a ConfigMap, and set `KUBERNETES_CLUSTERS_FILE` to its path in
`troubleshooter-config`. Remove `KUBERNETES_CLUSTER_NAME` and
`KUBERNETES_ALLOWED_NAMESPACES` from that ConfigMap. An entry without
`kubeconfig` reads the cluster the app runs in. Kubeconfigs for other clusters
go in a Secret mounted the same way. The [kind tests](kubernetes-testing.md)
apply this layout: one in-cluster entry, and one whose kubeconfig comes from a
mounted Secret.

```yaml
# kustomization.yaml next to the base manifests
configMapGenerator:
  - name: troubleshooter-clusters
    namespace: troubleshooter
    files: [clusters.yaml]
secretGenerator:
  - name: troubleshooter-kubeconfigs
    namespace: troubleshooter
    files: [prod.yaml]   # referenced in clusters.yaml as /etc/troubleshooter-kubeconfigs/prod.yaml
patches:
  - target: {kind: Deployment, name: troubleshooter}
    patch: |-
      - op: add
        path: /spec/template/spec/volumes
        value: [{name: clusters, configMap: {name: troubleshooter-clusters}}, {name: kubeconfigs, secret: {secretName: troubleshooter-kubeconfigs}}]
      - op: add
        path: /spec/template/spec/containers/0/volumeMounts
        value: [{name: clusters, mountPath: /etc/troubleshooter, readOnly: true}, {name: kubeconfigs, mountPath: /etc/troubleshooter-kubeconfigs, readOnly: true}]
```
