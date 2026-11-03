#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

artifact=${1:?artifact directory}
repository=${2:?image repository}
output=${3:?new output directory}
[[ "$repository" =~ ^ghcr\.io/[a-z0-9._-]+/[a-z0-9._-]+$ ]]
digest=$(jq -r .manifest_digest "$artifact/metadata.json")
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
work=$(mktemp -d "${TMPDIR:-/tmp}/akt-manifests.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
cp -R "$(dirname "$0")/../deployments" "$work/deployments"
mkdir "$output"
for variant in kubernetes kubernetes-authenticated; do
  cat >> "$work/deployments/$variant/kustomization.yaml" <<EOF
images:
  - name: ghcr.io/kmpoltorak/ai-kubernetes-troubleshooter
    newName: $repository
    digest: $digest
EOF
  kubectl kustomize "$work/deployments/$variant" > "$output/$variant.yaml"
done
