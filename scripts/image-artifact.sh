#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

command=${1:?create, verify or publish}
dir=${2:?artifact directory}
image="$dir/image.oci.tar"
metadata="$dir/metadata.json"
work=$(mktemp -d "${TMPDIR:-/tmp}/akt-artifact.XXXXXXXX")
trap 'rm -rf "$work"' EXIT

hash() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
valid_version() { [[ "$1" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; }
verify() {
  jq -e '
    .schema == 1 and
    (.version | type == "string") and
    (.revision | test("^[0-9a-f]{40}$")) and
    (.built_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
    ((.built_at | fromdateiso8601 | todateiso8601) == .built_at) and
    (.archive_sha256 | test("^[0-9a-f]{64}$")) and
    (.manifest_digest | test("^sha256:[0-9a-f]{64}$")) and
    (.config_digest | test("^sha256:[0-9a-f]{64}$"))
  ' "$metadata" >/dev/null
  version=$(jq -r .version "$metadata")
  if [ "$version" != ci ] && ! valid_version "$version"; then echo 'invalid version' >&2; exit 2; fi
  [ "$(hash "$image")" = "$(jq -r .archive_sha256 "$metadata")" ]
  skopeo --command-timeout 2m inspect --raw "oci-archive:$image" > "$work/manifest.json"
  [ "sha256:$(hash "$work/manifest.json")" = "$(jq -r .manifest_digest "$metadata")" ]
  [ "$(jq -r .config.digest "$work/manifest.json")" = "$(jq -r .config_digest "$metadata")" ]
  skopeo --command-timeout 2m inspect --config "oci-archive:$image" > "$work/config.json"
  jq -e --slurpfile m "$metadata" '
    .os == "linux" and .architecture == "amd64" and
    .config.Labels["org.opencontainers.image.version"] == $m[0].version and
    .config.Labels["org.opencontainers.image.revision"] == $m[0].revision and
    .config.Labels["org.opencontainers.image.created"] == $m[0].built_at
  ' "$work/config.json" >/dev/null
}

case "$command" in
  create)
    version=${3:?version}
    revision=${4:?revision}
    built_at=${5:?build time}
    if [ "$version" != ci ] && ! valid_version "$version"; then echo 'expected ci or a SemVer x.y.z' >&2; exit 2; fi
    [[ "$revision" =~ ^[0-9a-f]{40}$ ]]
    [[ "$built_at" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]
    jq -en --arg stamp "$built_at" '($stamp | fromdateiso8601 | todateiso8601) == $stamp' >/dev/null
    mkdir "$dir"
    docker buildx build --platform linux/amd64 --provenance=false --sbom=false \
      --build-arg "BUILD_VERSION=$version" --build-arg "BUILD_REVISION=$revision" \
      --build-arg "BUILD_TIME=$built_at" \
      --output "type=oci,name=ghcr.io/kmpoltorak/ai-kubernetes-troubleshooter:candidate,dest=$image" .
    skopeo --command-timeout 2m inspect --raw "oci-archive:$image" > "$work/manifest.json"
    jq -n --arg version "$version" --arg revision "$revision" --arg built_at "$built_at" \
      --arg archive "$(hash "$image")" --arg manifest "sha256:$(hash "$work/manifest.json")" \
      --arg config "$(jq -r .config.digest "$work/manifest.json")" \
      '{schema:1,version:$version,revision:$revision,built_at:$built_at,archive_sha256:$archive,manifest_digest:$manifest,config_digest:$config}' > "$metadata"
    verify
    ;;
  verify) verify ;;
  publish)
    verify
    valid_version "$version"
    repository=${3:?destination repository}
    [[ "$repository" =~ ^[a-z0-9][a-z0-9./:_-]+$ ]]
    ref="$repository:$version"
    skopeo --command-timeout 10m copy --all --preserve-digests --retry-times 2 \
      --digestfile "$work/published.digest" "oci-archive:$image" "docker://$ref"
    [ "$(cat "$work/published.digest")" = "$(jq -r .manifest_digest "$metadata")" ]
    skopeo --command-timeout 2m inspect --raw "docker://$ref" > "$work/published.json"
    [ "sha256:$(hash "$work/published.json")" = "$(jq -r .manifest_digest "$metadata")" ]
    ;;
  *) echo 'expected create, verify or publish' >&2; exit 2 ;;
esac
