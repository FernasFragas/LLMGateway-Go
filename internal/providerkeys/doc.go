// Package providerkeys is the provider key cache: the outbound half of decision
// #1's two fail-static caches. It holds the credentials the gateway spends on
// its own calls to OpenAI and Anthropic, and it authenticates nobody — inbound
// identity is internal/auth's JWKS cache, fed by the kube-apiserver. The two
// share a pattern, never a failure domain, and neither outage implies the
// other's.
//
// One source per provider (ADR-001), each with its own path and its own
// cadence: a shared source could only be scheduled once, which would make
// every interval but the shortest decorative. Fanning the sourcing out also
// keeps blast radius at one provider — a mangled secret or an unreachable
// path costs exactly the provider it belongs to, and failover covers the gap.
//
// Fail static, on this cache, means:
//
//   - the request path only ever reads the cache; the network is touched by
//     Refresh alone, which main runs in the background;
//   - a failed refresh keeps the previous key — stale-but-usable beats
//     unavailable; the logs decorator gives the failure its line;
//   - a rejection never evicts. Sending no credential at all is strictly
//     worse than sending a stale one, so an upstream 401 schedules a refresh
//     (decision #8) and leaves the cached value in place until a fetch
//     succeeds.
//
// Absence is meaningful and common: a provider with no configured source has
// no key, KeyFor returns "", and its adapter sends no auth header. That is
// how a self-hosted, in-cluster Ollama route is declared — a valid steady
// state, never an error. Readiness follows: a gateway configured with no
// sources at all is ready immediately, because it has nothing to wait for.
//
// Keys are values, never logged. The logs decorator reports that a refresh
// failed, never what the cache holds.
package providerkeys
