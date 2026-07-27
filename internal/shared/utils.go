// Package shared holds what every provider adapter uses and none of them
// owns: the translation of failures into *gateway.ProviderFault. The
// adapters themselves live one package per provider (internal/ollama,
// internal/openai, internal/anthropic), each speaking its provider's wire
// dialect — but every failure they report goes through these builders, so
// the core classifies outcomes without knowing any protocol, and the
// Cause, which may carry raw provider text, goes to the logs and never to
// a caller (decision #4).
//
// The builders encode the accounting decision (#6) exactly once, where no
// adapter can diverge from it: a refusal — any non-2xx — costs nothing
// upstream and is therefore never bad_response, the kind that feeds the
// double-spend estimate. Only an exchange the provider may have billed (a
// 200 we cannot use) earns it.
//
// Three rules hold in every adapter that imports this package:
//
//   - never log: observability is the logs decorator's job;
//   - never retry: failover is the core's, and exactly one (decision #2);
//   - never outlive ctx: the core sets the per-try deadline, the adapter
//     aborts the outbound call when it fires.
//
// This package stays this small on purpose: a helper belongs here only
// when a second adapter would otherwise copy it and the copies must never
// disagree — anything else lives with its provider.
package shared

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// Key resolves the credential to send, and is called once per request —
// never captured at construction. Provider keys rotate under a running pod:
// each has its own source and cadence, and an upstream 401 schedules an
// out-of-band refresh (ADR-001). An adapter holding a string copied at
// wiring time would keep sending the rotated-out key until the process
// restarted — the load-once-never-refresh design that ADR exists to reject.
//
// An empty return means "send no credential". That is a steady state, not an
// error: a self-hosted, in-cluster Ollama route is authenticated by the
// network it sits on, and has no key to configure.
type Key func() string

// NoKey is the Key for a route that needs no credential, and the fallback
// when an adapter is constructed with a nil Key.
func NoKey() string { return "" }

// StaticKey is a Key that never changes — for a fixed credential and for
// tests. Production wiring passes the secret cache's accessor instead, or
// rotation stops at the adapter's door.
func StaticKey(key string) Key { return func() string { return key } }

// TransportFault classifies a request that produced no response: the
// per-try deadline firing is a timeout; everything else — DNS, TLS, refused
// connections — is unreachable, zero bytes sent.
func TransportFault(err error) *gateway.ProviderFault {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &gateway.ProviderFault{Kind: gateway.FaultTimeout, Cause: err}
	}

	return &gateway.ProviderFault{Kind: gateway.FaultUnreachable, Cause: err}
}

// StatusFault classifies a non-2xx answer. 429 keeps its own kind so the
// core's accounting can tell provider throttling apart; every other refusal
// is server_error — including 4xx, because the distinction that matters
// here is billing, and a refusal bills nothing.
func StatusFault(provider string, status int, body []byte) *gateway.ProviderFault {
	kind := gateway.FaultServerError
	switch status {
	case http.StatusTooManyRequests:
		kind = gateway.FaultThrottled
	case http.StatusUnauthorized, http.StatusForbidden:
		// The provider refused *our* credential, not the caller's. Naming it
		// separately is what lets the wiring schedule a key refresh instead
		// of waiting out the interval (decision #8).
		kind = gateway.FaultRejected
	}

	return &gateway.ProviderFault{
		Kind:  kind,
		Cause: fmt.Errorf("%s: status %d: %s", provider, status, bytes.TrimSpace(body)),
	}
}

// BadResponseFault marks a 200 the gateway cannot use — the provider likely
// billed it, so whatever usage could still be parsed rides along to tighten
// the double-spend estimate.
func BadResponseFault(provider string, cause error, observed *gateway.Usage) *gateway.ProviderFault {
	return &gateway.ProviderFault{
		Kind:          gateway.FaultBadResponse,
		ObservedUsage: observed,
		Cause:         fmt.Errorf("%s: %w", provider, cause),
	}
}
