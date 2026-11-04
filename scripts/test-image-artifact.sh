#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

artifact=$(cd "${1:?artifact directory}" && pwd)
tool=$(cd "$(dirname "$0")" && pwd)/image-artifact.sh
work=$(mktemp -d "${TMPDIR:-/tmp}/akt-artifact-test.XXXXXXXX")
trap 'rm -rf "$work"' EXIT

bash "$tool" verify "$artifact"
# Exercise the same digest-preserving copy used for publication.
skopeo --command-timeout 5m copy --all --preserve-digests \
  "oci-archive:$artifact/image.oci.tar" "oci-archive:$work/copied.tar"
skopeo --command-timeout 2m inspect --raw "oci-archive:$work/copied.tar" > "$work/copied.json"
test "sha256:$(shasum -a 256 "$work/copied.json" | cut -d ' ' -f 1)" = "$(jq -r .manifest_digest "$artifact/metadata.json")"

mkdir "$work/bad"
cp "$artifact/image.oci.tar" "$work/bad/image.oci.tar"
cp "$artifact/metadata.json" "$work/bad/metadata.json"
printf 'corrupted' >> "$work/bad/image.oci.tar"
if bash "$tool" verify "$work/bad"; then echo 'accepted changed archive' >&2; exit 1; fi
cp "$artifact/image.oci.tar" "$work/bad/image.oci.tar"
for mutation in '.config_digest="sha256:0000000000000000000000000000000000000000000000000000000000000000"' \
  '.revision="untrusted"' '.version="v1.0.0"' '.built_at="unknown"' '.built_at="2026-02-30T12:00:00Z"' '.schema=2'; do
  jq "$mutation" "$artifact/metadata.json" > "$work/bad/metadata.json"
  if bash "$tool" verify "$work/bad"; then echo "accepted invalid metadata: $mutation" >&2; exit 1; fi
done
echo 'PASS: digest-preserving copy and rejection paths'
