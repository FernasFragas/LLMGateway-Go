// Package config parses the gateway's one config file and hands each layer
// its own terms: api.Config for the transport, gateway.Config for routing
// and budgets, auth.Config for identity, and the JWKS knobs main's refresh
// loop needs.
//
// The file is YAML, read strictly (KnownFields): unknown keys are rejected,
// never dropped — the rule the wire contract lives by holds for the
// operator's file too; a typo must fail the boot, not silently change a
// policy. Durations are Go duration strings ("90s", "2m"). YAML because the
// file's audience lives in Kubernetes: it ships in a ConfigMap beside YAML
// manifests, and comments — why a deadline is what it is — carry the same
// decisions-written-down value the rest of this repo insists on. The parser
// is the module's one dependency, a deliberate trade.
//
// Zero in any per-app limit — rps, tokens_per_minute, max_in_flight, or a
// limits: block omitted entirely — means that currency is unlimited for that
// app: a caller pays for what it configures, not for what it omits, matching
// global_max_in_flight's own zero-means-unenforced convention. All three
// currencies read it the same way, so no caller can be metered
// inconsistently across them.
//
// Load validates only what the file alone can prove: it parses, policies
// name real kinds and carry an allowlist exactly when the kind reads one,
// no two apps share a name or a subject, every model-provider row is
// complete. Each consumer's constructor keeps judging its own invariants —
// checking them twice here would fork the contract. Omitted tuning stays
// zero and takes the consumer's defaults.
package config
