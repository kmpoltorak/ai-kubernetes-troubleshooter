# Build metadata

`GET /health` and `GET /ready` include a public `build` object:

```json
{"version":"dev","revision":"unknown","built_at":null}
```

The existing `status`, readiness `checks` and HTTP codes retain their meaning.
Metadata is included in `503` readiness responses and remains public when
authentication is enabled. It contains only these three build fields, without
runtime configuration, credentials or dependency details.

The UI uses its existing readiness request to display the responding replica's
version and the first twelve characters of its revision. Its accessible label
includes the full revision and build time when known. An older backend without
metadata remains usable. During a rolling update this identifies one response;
it does not establish that every replica runs the same version.

## Supply metadata

Plain `go run`, `make build` and Docker builds default to `dev`, `unknown` and
an unknown build time. Values are embedded at compilation, with no Git command
or environment lookup in the running application. To stamp a binary:

```bash
make build BUILD_VERSION=0.1.0-test \
  BUILD_REVISION="$(git rev-parse HEAD)" BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

For an image, pass the same values as Docker build arguments:

```bash
docker build -t troubleshooter:local \
  --build-arg BUILD_VERSION=0.1.0-test \
  --build-arg BUILD_REVISION="$(git rev-parse HEAD)" \
  --build-arg BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)" .
```

| Argument | Default | OCI image label |
|---|---|---|
| `BUILD_VERSION` | `dev` | `org.opencontainers.image.version` |
| `BUILD_REVISION` | `unknown` | `org.opencontainers.image.revision` |
| `BUILD_TIME` | empty | `org.opencontainers.image.created` |

Use an RFC3339 build time, preferably UTC. Empty or invalid timestamps produce
`built_at: null` in the API; the OCI label retains the supplied value. Changing
runtime environment variables does not change compiled metadata. Use only
version, revision and time values in these arguments: they are public.

Metadata records what the builder supplied; it does not verify source identity
or uncommitted changes. A version string does not itself publish an image.
