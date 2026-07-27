// Package auth is the AppDirectory adapter for bound Kubernetes
// ServiceAccount tokens: callers present the short-lived, audience-bound JWT
// the kubelet projects into their pod, and the gateway verifies it locally —
// never a TokenReview or introspection call on the request path.
//
// The JWKSCache here is the inbound half of decision #1's two fail-static
// caches: it holds the cluster's token-signing keys and is the only thing
// that authenticates a caller. The outbound half — the provider key cache fed
// by SecretSource (ADR-001) — is a separate cache with its own source, its own
// TTL, and its own readiness check; it authenticates nobody. The two share a
// pattern, never a failure domain, and neither one's outage implies the
// other's. Fail static, on this cache, means:
//
//   - the request path reads only the in-memory JWKS cache; the network is
//     touched by Refresh alone, which main runs in the background;
//   - a failed refresh keeps the previous signing keys — stale-but-usable
//     beats unavailable; the logs decorator gives the failure its line. The
//     bounded cost is a cluster signing-key rotation the cache hasn't seen
//     yet: those tokens carry an unknown kid and are refused 401 until a
//     refresh succeeds;
//   - a cold instance that has never loaded signing keys refuses readiness
//     (JWKSCache.Ready is health.Check-shaped), so it takes no traffic
//     instead of rejecting every token.
//
// Verification is deliberately narrow: RS256 only — the kube-apiserver signs
// ServiceAccount tokens with RSA, and a one-algorithm allowlist is the
// cheapest defense against alg-confusion attacks; a second algorithm is a
// config decision for the day a cluster needs it. Issuer and audience must
// match the configuration exactly, expiry is honored with a small leeway for
// clock skew, and the subject ("system:serviceaccount:<namespace>:<name>")
// is the app's identity: Config.Apps maps it to the gateway.App whose terms
// the core enforces. The token itself is a credential and is never logged,
// the same rule provider keys live under.
//
// Wiring in main: build the JWKSCache, register Ready on the health checker
// under "jwks", run Refresh on a ticker through the logs/auth decorator, and
// hand the Directory to the core wrapped in logs/gateway's AppDirectory — the
// core and the edge never learn that keys became tokens.
package auth
