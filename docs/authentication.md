# Authentication

`AUTH_ENABLED=false` runs without login and without contacting OAuth2 Proxy or
an identity provider. This is the default for local development, tests and the
base minikube deployment. Restart the application after changing the setting.
Use it only where clients with network access may read all incident data.

`AUTH_ENABLED=true` delegates credential validation to OAuth2 Proxy. The backend
checks UI, API and metrics requests itself, so bypassing an ingress does not
bypass authentication. `/health` and `/ready` are public and contain no incident
data; readiness checks the authentication service when authentication is enabled.

## OIDC provider

Register an OIDC client with the callback
`https://troubleshooter.internal.example.com/oauth2/callback`, replacing the host
with your installation's public host. Configure these values through the
environment or Kubernetes Secrets:

| Setting | Meaning |
|---|---|
| `OIDC_ISSUER_URL` | issuer with OIDC discovery and JWKS |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | registered client credentials |
| `AUTH_COOKIE_SECRET` | 32 random bytes encoded as URL-safe base64; shared across proxy replicas |
| `OIDC_ALLOWED_GROUPS` | allowed OIDC groups; default `troubleshooter-users` |
| `AUTH_PUBLIC_URL` | exact public origin, for example `https://troubleshooter.internal.example.com` |

The provider must supply `sub`, verified `email` and the allowed `groups` claim.
The supplied [proxy configuration](../deployments/kubernetes-authenticated/oauth2-proxy.cfg)
uses authorization code flow with PKCE and nonce validation. Browser cookies
are Secure, HttpOnly, host-only, SameSite=Lax and expire after eight hours.
Group changes are not immediate session revocation; existing sessions can remain
valid until they expire. Rotate the cookie secret to invalidate all proxy sessions.

Generate a cookie secret into an environment variable without printing it:

```bash
export AUTH_COOKIE_SECRET="$(openssl rand -base64 32 | tr '+/' '-_')"
```

Keep client secrets and the cookie secret in the environment or secret store.
Do not put them in manifests, screenshots, shared commands or source control.

## Docker Compose

Set `AUTH_ENABLED=true`, `AUTH_PUBLIC_URL` and the OIDC settings in the local
`.env` or exported environment, then run:

```bash
docker compose --profile auth up --build
```

Terminate HTTPS at your reverse proxy and forward to the application's port
8080. OAuth2 Proxy is an internal service with no published port. Its callback
URL is derived from `AUTH_PUBLIC_URL`; client-provided forwarding headers do not
choose that URL. Configure your TLS endpoint to redirect or reject plain HTTP.

To disable login, set `AUTH_ENABLED=false` and recreate the app. The `auth`
profile can be omitted entirely; an unavailable proxy does not affect the app
when authentication is disabled.

## Kubernetes

The base `deployments/kubernetes` configuration has authentication disabled.
For an authenticated installation, use `deployments/kubernetes-authenticated`.
Set its public URL and TLS host to your installation's host. The overlay uses
the cluster's default ingress controller; set `ingressClassName` in your own
overlay if needed. Create the TLS Secret `troubleshooter-tls` and configure
your controller to redirect or reject HTTP; TLS termination is external to the app.

First create the namespace, then the authentication Secret from environment
variables populated by your secret manager:

```bash
kubectl apply -f deployments/kubernetes/namespace.yaml
kubectl -n troubleshooter create secret generic troubleshooter-auth \
  --from-literal=OAUTH2_PROXY_OIDC_ISSUER_URL="$OIDC_ISSUER_URL" \
  --from-literal=OAUTH2_PROXY_CLIENT_ID="$OIDC_CLIENT_ID" \
  --from-literal=OAUTH2_PROXY_CLIENT_SECRET="$OIDC_CLIENT_SECRET" \
  --from-literal=OAUTH2_PROXY_COOKIE_SECRET="$AUTH_COOKIE_SECRET" \
  --from-literal=OAUTH2_PROXY_ALLOWED_GROUPS="${OIDC_ALLOWED_GROUPS:-troubleshooter-users}" \
  --from-literal=OAUTH2_PROXY_REDIRECT_URL="$AUTH_PUBLIC_URL/oauth2/callback"
kubectl apply -k deployments/kubernetes-authenticated
```

The database Secret and namespace RoleBindings are still required as described
in the README. OAuth2 Proxy runs in a separate Deployment without a Kubernetes
ServiceAccount token. A NetworkPolicy restricts its port to application pods
in the same namespace; this requires a CNI that enforces NetworkPolicy.

The switch also works on an existing installation:

```bash
kubectl -n troubleshooter set env deployment/troubleshooter AUTH_ENABLED=false
kubectl -n troubleshooter rollout status deployment/troubleshooter --timeout=120s
# Restore the authentication check once the OIDC service is configured:
kubectl -n troubleshooter set env deployment/troubleshooter AUTH_ENABLED=true
```

With authentication off, no identity-provider configuration is needed by the
application, even if the separate proxy Deployment is unavailable. The base
configuration can be used directly for a local minikube installation.

## Browser, API and metrics clients

Opening `/` starts OIDC login. A session lets the UI use the API. Cookie-based
mutations require an `Origin` matching `AUTH_PUBLIC_URL`; API clients instead
send `Authorization: Bearer <JWT>`. The proxy checks signature, issuer, audience,
expiry and allowed groups. Invalid bearer tokens do not fall back to cookies.

Unauthenticated API/metrics requests return `401`; rejected groups may return
`403`. An unavailable/misconfigured authentication service returns `503`.
The application logs only the verified user identifier, never bearer tokens or
session cookies. With authentication enabled, all routes except probes are rate
limited. Rate limiting uses the direct peer address, so an ingress
may share one bucket across users; arbitrary forwarding headers are not trusted.

Prometheus needs an OIDC JWT with the same admission claims when scraping
`/metrics`; the default monitoring profile is for authentication-disabled demos.
Configure token acquisition/refresh in the scraper or its credential manager.
The application does not mint or refresh API tokens. All admitted users can read
the installation's incidents; there are no per-user or per-namespace roles.

After a session expires, reload `/` to sign in again. `/oauth2/sign_out` clears
the local session; logging out of the identity provider is a separate operation.

## Check the installation

After enabling authentication, an anonymous `GET /api/v1/incidents` must return
`401`, and `/ready` must include `authentication: ok`. Do not rely on starting
the Compose `auth` profile alone: `AUTH_ENABLED` controls access checks.
