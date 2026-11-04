# Image candidates and releases

CI builds one Linux amd64 OCI archive. The Kubernetes jobs (1.37.0 on pull
requests, 1.36.4 and 1.37.0 on `main` and releases) download that same artifact,
verify its checksum and image metadata, and import it directly into kind. They do
not rebuild the app.
The loaded configuration ID must match the artifact, the Deployment uses the
original manifest digest, and the live tests check the compiled build fields.

Each `tested-image-*` artifact contains `image.oci.tar` and `metadata.json` with
the archive SHA256, OCI manifest and configuration digests, source revision,
version and build time. CI retains it for three days. The candidate is not a
published release. The image is not signed; metadata is supplied by the builder.

## Versions and the release PR

Versions follow SemVer and are derived by
[release-please](https://github.com/googleapis/release-please-action) from commit
prefixes since the last release: `fix:` → patch, `feat:` → minor, `!` after the type
or a `BREAKING CHANGE:` footer → major. `docs:`, `ci:` and `chore:` alone do not
release. After each push to `main`, release-please keeps one open release PR with the
next version and its `CHANGELOG.md` entry. Merging that PR is the decision to release.

Requirements and limits:

- Settings → Actions → General must allow GitHub Actions to create pull requests.
- The release PR is opened with the workflow token, so GitHub runs no CI on it. It
  changes only `CHANGELOG.md` and `.release-please-manifest.json`; the full CI runs
  on its merge commit before anything is published.

## Publish

Merging the release PR creates a draft release `vX.Y.Z` and its tag at the merge
commit. The **Release** workflow then:

1. requires the version to be `X.Y.Z` and the tag to point at the pushed commit;
2. runs the whole CI on that commit, including authentication, database
   integration, browser, security and both Kubernetes versions;
3. copies the tested OCI archive to
   `ghcr.io/kmpoltorak/ai-kubernetes-troubleshooter:X.Y.Z`, requiring its digest to
   be preserved, and verifies the remote manifest;
4. attaches `metadata.json` and the base/auth Kubernetes manifests pinned to the
   image digest, adds the digest to the release notes, and publishes the draft.

Only the publish job gets package and repository write permissions, and only after
all checks pass. No `latest` tag is created. Registry tags can be changed by anyone
with write access: install by the digest in the release assets.

If a check fails, the draft release and its tag remain and nothing is published.
Inspect them, then delete both, or fix the problem and let the next release PR use
the next version. Image publication, asset upload and publishing the draft are
separate operations: a failure between them can leave an image without a public
release. Check the image digest and the draft before rerunning.

## Install a release

Download `kubernetes.yaml` or `kubernetes-authenticated.yaml` from the selected
GitHub release. Review and configure the namespace policy, database Secret,
ingress and, for authentication, the [OIDC settings](authentication.md) before
applying it. The base variant has authentication disabled. PostgreSQL is external;
release assets contain neither database credentials nor auth secrets. The image
reference is already pinned; do not replace it with a floating tag.

## Local verification

`scripts/image-artifact.sh create DIR VERSION REVISION UTC_TIME` exports a new
candidate from the current working tree. It requires Docker Buildx with OCI
export support, Skopeo, jq and shasum. Versions are `ci` or SemVer `X.Y.Z`; revision is
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
