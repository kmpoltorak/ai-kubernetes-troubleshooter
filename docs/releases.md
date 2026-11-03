# Image candidates and releases

CI builds one Linux amd64 OCI archive after lint, Go unit tests and UI checks.
The two Kubernetes jobs download that same artifact, verify its checksum and
image metadata, and import it directly into kind. They do not rebuild the app.
The loaded configuration ID must match the artifact, the Deployment uses the
original manifest digest, and the live tests check the compiled build fields.

Each `tested-image-*` artifact contains `image.oci.tar` and `metadata.json` with
the archive SHA256, OCI manifest and configuration digests, source revision,
version and build time. CI retains it for three days. The candidate is not a
published release. The image is not signed; metadata is supplied by the builder.

## Prepare a candidate

Run the **Release candidate** workflow from `main`, with an unused version
`0.x.y` (without `v`) and **publish disabled**. It runs the same CI workflow,
including authentication, database integration, security and both Kubernetes
versions. Download its artifact to inspect it. This mode creates no registry
image, Git tag or GitHub release.

## Publish

After reviewing the source and choosing a version, run **Release candidate**
from `main` with that version and **publish enabled**. This starts a fresh,
fully tested candidate; it does not promote an earlier workflow run. Only after
all its checks pass does the publication job receive package/repository write
permissions. PR workflows have read-only permissions.

The publication job copies the original OCI archive to
`ghcr.io/kmpoltorak/ai-kubernetes-troubleshooter:0.x.y`, requires preservation
of its digest, and verifies the remote manifest. It then creates a GitHub
prerelease and `v0.x.y` tag at the tested commit, with metadata and base/auth
Kubernetes manifests pinned to the image digest. It creates no `latest` tag.

Registry tags can be changed. Install by the digest in the release assets,
and never reuse a version for different source. The workflow rejects an existing
Git release tag and serializes release runs. It cannot prevent another registry
writer from changing a tag.
Tag creation also refuses a version taken while the tests were running; release
creation requires that tag instead of silently choosing an existing one.

Publication, Git tagging and GitHub release creation are separate operations.
A failure can leave a registry image or a Git tag without release assets. Inspect the image
digest and Git tag before recovery; do not blindly rerun a version with different
source or a different candidate. Package visibility and repository permissions
must allow your consumers to pull the image. No public image is implied merely
by merging the workflow.

## Install a release

Download `kubernetes.yaml` or `kubernetes-authenticated.yaml` from the selected
GitHub prerelease. Review and configure the namespace policy, database Secret,
ingress and, for authentication, the [OIDC settings](authentication.md) before
applying it. The base variant has authentication disabled. PostgreSQL is external;
release assets contain neither database credentials nor auth secrets. The image
reference is already pinned; do not replace it with a floating tag.

## Local verification

`scripts/image-artifact.sh create DIR VERSION REVISION UTC_TIME` exports a new
candidate from the current working tree. It requires Docker Buildx with OCI
export support, Skopeo, jq and shasum. Versions are `ci` or `0.x.y`; revision is
a full lowercase Git SHA and time has the form `2026-01-01T12:34:56Z`.

```bash
bash scripts/image-artifact.sh verify /path/to/candidate
bash scripts/test-image-artifact.sh /path/to/candidate
E2E_IMAGE_ARTIFACT=/path/to/candidate make test-e2e
```

The artifact test checks a digest-preserving OCI copy and rejection of changed
archives and invalid metadata. The kind runner additionally checks import and
the running binary. [Docker's OCI exporter](https://docs.docker.com/build/exporters/oci-docker/)
and [Skopeo's copy documentation](https://github.com/podman-container-tools/skopeo/blob/main/docs/skopeo-copy.1.md)
describe the archive format and digest-preserving operation.
