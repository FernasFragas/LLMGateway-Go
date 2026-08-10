// Package metrics counts what the gateway core does, one decorator per port.
// Every decorator forwards each call to the next unchanged — typically the
// logging decorator — so one call both meters and narrates, and the two can
// never disagree about what happened.
//
// # The cardinality rule
//
// A counter may carry a dimension when that dimension's values are a closed
// set known before any request arrives. Three qualify: the fixed ErrorCode
// and FaultKind tables declared in internal/gateway, and the app and provider
// names read from config at boot. Nothing else does, and in particular
// nothing a caller supplies ever becomes a label — an unrecognized API key
// resolves to no app, so it cannot reach a counter at all.
//
// The rule is enforced by construction rather than by discipline.
// keyedCounter takes its key set once, at build time, and silently drops
// anything outside it; there is no path that creates a series at runtime. A
// deployment's series count is therefore a property of its config file, and
// an operator can read the upper bound off it: apps × codes for refusals,
// providers × kinds for upstream failures, and one per app for the rest.
//
// This replaces an earlier and more cautious rule — that app and provider
// were not bounded enough to carry at all — which cost more than it saved.
// It made the design's central promise unmeasurable: "spend is traceable per
// app, and it reconciles with the provider bills" cannot be checked from a
// process-wide token total, and the reconciliation criterion was written
// against a labelled series that did not exist.
//
// # What this package does not do
//
// No histograms, no timing, no request-path work beyond an atomic add and a
// map read. Latency belongs to the exporter's own instruments; these
// counters stay plain atomics behind accessor methods so nothing here
// depends on a metrics SDK, and internal/metrics/otlp is the only package
// that knows one exists.
package metrics
