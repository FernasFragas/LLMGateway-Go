// Package auth is the AppDirectory adapter for bound Kubernetes
// ServiceAccount tokens: callers present the short-lived, audience-bound JWT
// the kubelet projects into their pod, and the gateway verifies it locally —
// never a TokenReview or introspection call on the request path.
//
// Fail static (decision #1) holds exactly as it did for API keys:
//
//   - the request path reads only the in-memory key cache; the network is
//     touched by Refresh alone, which main runs in the background;
//   - a failed refresh keeps the previous keys — stale-but-usable beats
//     unavailable; the logs decorator gives the failure its line;
//   - a cold instance with no keys refuses readiness (KeyCache.Ready is
//     health.Check-shaped), the same place the empty key cache refused it
//     before.
//
// Verification is deliberately narrow: RS256 only — the kube-apiserver signs
// ServiceAccount tokens with RSA, and a one-algorithm allowlist is the
// cheapest defense against alg-confusion attacks; a second algorithm is a
// config decision for the day a cluster needs it. Issuer and audience must
// match the configuration exactly, expiry is honored with a small leeway for
// clock skew, and the subject ("system:serviceaccount:<namespace>:<name>")
// is the app's identity: Config.Apps maps it to the gateway.App whose terms
// the core enforces. The token itself is a credential and is never logged —
// the rule that held for API keys holds here.
//
// Wiring in main: build the KeyCache, register Ready on the health checker,
// run Refresh on a ticker through the logs/auth decorator, and hand the
// Directory to the core wrapped in logs/gateway's AppDirectory — the core
// and the edge never learn that keys became tokens.
package auth
