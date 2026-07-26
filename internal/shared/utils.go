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
	if status == http.StatusTooManyRequests {
		kind = gateway.FaultThrottled
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
