# Security policy

Only the latest released version is supported. Do not report vulnerabilities
in a public issue. Use **Security → Advisories → New draft security advisory**
in the GitHub repository. If that is unavailable, contact the repository owner
through an established private channel.

We aim to acknowledge reports within three business days, provide an initial
assessment within seven business days, and coordinate remediation and
disclosure. These response targets are not service-level guarantees. Include
affected versions, impact, reproduction steps, and any proposed mitigation.

## Deployment boundary

Bind `memoryd` to loopback and terminate TLS at a narrowly scoped reverse
proxy. PostgreSQL and the embedding service must remain private. The supplied
Caddy configuration does not proxy `/healthz`, `/readyz`, or `/metrics`.
Configure firewall and monitoring access explicitly if those endpoints must be
reachable outside the host.

ZestMem is not end-to-end encrypted. TLS protects network transit; PostgreSQL
administrators, the `memoryd` process account, and host root can read memory
content. Treat database backups, logs, API keys, OIDC client credentials, and
browser sessions as sensitive. Store secrets in protected files or a secret
manager, not command lines, unit definitions, images, or source control.

## MCP and REST authentication

MCP and `/api/v1/*` accept independently revocable native API keys or OIDC
resource-server access tokens. Validate an exact issuer and audience, use a
stable subject claim, and bind `(issuer, subject)` to one local Principal. Each
agent should have its own credential and only the Space grants it needs. Browser
identity is never accepted as MCP authority merely because a user can open the
console.

RFC 9728 metadata is public discovery, not authorization. Rate limiting is
applied after bearer verification and is keyed by the authenticated Principal.
Do not place a perimeter login page in front of machine MCP paths; the product
resource server must validate every request itself.

## Browser authentication

The console supports three entry modes:

1. A native API key is exchanged by `/console/auth/token` for an opaque browser
   session. JavaScript does not retain the bearer credential.
2. Native OIDC uses Authorization Code flow with PKCE, state, and nonce.
   ZestMem validates the ID token and resolves its exact `(issuer, subject)`
   through the local Principal binding.
3. A trusted identity-aware edge may forward a JWT assertion. Configure its
   exact issuer, audience, JWKS URL, and a header that only the trusted proxy can
   set. Strip any client-supplied copy before adding the verified assertion.

Assertion and native-OIDC deployments require an exact HTTPS
`CONSOLE_PUBLIC_ORIGIN`; HTTP is accepted only for loopback development. The
native OIDC callback must be exactly
`CONSOLE_PUBLIC_ORIGIN/console/auth/oidc/callback`. Public cookies are `Secure`,
`HttpOnly`, `SameSite`, use `__Host-` names, and are scoped to `Path=/`.

OIDC discovery is lazy and retryable, so an unavailable IdP does not stop the
memory service. Login remains unavailable until discovery succeeds. Encrypted
flow data is carried in short-lived state cookies; only bounded consumed-state
replay markers remain in memory. Browser sessions are bounded per Principal,
never outlive the source credential, and external sessions revalidate their
local binding on every request. Disabling or unbinding a Principal therefore
revokes an existing browser session on its next request.

Logout revokes the local ZestMem session. If a deployment also needs upstream
single logout, end the IdP or access-proxy session separately.

All memory content and metadata are untrusted input even when written by an
authenticated Principal. Authentication establishes audit identity and Space
authorization; it does not make retrieved content safe instructions.

API-key browser login has a configurable process-wide rate backstop, while
OIDC token exchange has a separate concurrency bound. Public deployments
should also enforce per-client limits for every auth route at a trusted
reverse proxy. Browser sessions, replay markers, and OIDC flow keys are
process-local; use one UI replica or sticky routing, and expect a restart to
invalidate browser sessions and in-progress logins.

Do not attach databases, token files, raw memory content, client secrets, or
authorization-bearing proxy logs to an issue. Remove credentials from any
reproduction and rotate a credential immediately if it may have been exposed.
