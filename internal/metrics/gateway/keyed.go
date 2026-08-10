package metrics

import (
	"sync/atomic"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// keyedCounter is one counter split across a key set fixed at construction.
//
// The map is written once and only read afterwards, so a lookup needs no
// lock and no allocation — which is what makes a dimension affordable on the
// request path. It also replaces the mutex the by-code breakdown used to
// need: a fixed key set turns a guarded map into a plain read.
//
// A key outside the set is dropped rather than created. That is the
// cardinality rule of doc.go made structural: no request can mint a series,
// whatever it carries.
type keyedCounter[K comparable] struct {
	byKey map[K]*atomic.Int64
}

func newKeyedCounter[K comparable](keys []K) *keyedCounter[K] {
	byKey := make(map[K]*atomic.Int64, len(keys))
	for _, key := range keys {
		byKey[key] = new(atomic.Int64)
	}

	return &keyedCounter[K]{byKey: byKey}
}

// add credits n to key. An unknown key is ignored: it cannot happen for a
// key that came from config, and inventing an entry for one that did would
// be exactly the unbounded growth this type exists to prevent.
func (c *keyedCounter[K]) add(key K, n int64) {
	if v, ok := c.byKey[key]; ok {
		v.Add(n)
	}
}

// get reports one key's count; an unknown key reads as zero.
func (c *keyedCounter[K]) get(key K) int64 {
	if v, ok := c.byKey[key]; ok {
		return v.Load()
	}

	return 0
}

// total sums every key, so a process-wide view costs one pass over a map
// whose size is the config's — read at most once per collection cycle, never
// on the request path.
func (c *keyedCounter[K]) total() int64 {
	var sum int64
	for _, v := range c.byKey {
		sum += v.Load()
	}

	return sum
}

// appCode and providerKind are the two composite dimensions. Both are the
// cross product of two closed sets, enumerated at construction so the series
// count is fixed before the first request.
type appCode struct {
	app  string
	code gateway.ErrorCode
}

type providerKind struct {
	provider string
	kind     gateway.FaultKind
}

// ErrorCodes and FaultKinds are the fixed tables the breakdowns enumerate.
// They live here rather than in the exporter because they are what bounds
// this package's own storage; the exporter reads the same lists back so the
// two can never enumerate different sets.
var ErrorCodes = []gateway.ErrorCode{
	gateway.CodeInvalidRequest,
	gateway.CodeUnauthorized,
	gateway.CodeQuotaExceeded,
	gateway.CodeConcurrencyCeiling,
	gateway.CodeUpstreamFailed,
	gateway.CodeModelUnavailable,
	gateway.CodeGatewayTimeout,
}

// FaultKinds includes the unclassified bucket, which is not a domain value:
// a provider adapter returning a bare error still has to land somewhere, and
// a dropped failure is worse than an ugly label.
var FaultKinds = []gateway.FaultKind{
	gateway.FaultUnreachable,
	gateway.FaultTimeout,
	gateway.FaultServerError,
	gateway.FaultThrottled,
	gateway.FaultBadResponse,
	gateway.FaultRejected,
	KindUnclassified,
}

// KindUnclassified is where a provider error that is not a *ProviderFault is
// counted. Seeing it in a dashboard means an adapter is not reporting in the
// core's terms — a bug in that adapter, not in the provider.
const KindUnclassified = gateway.FaultKind("unclassified")

func appCodeKeys(apps []string) []appCode {
	keys := make([]appCode, 0, len(apps)*len(ErrorCodes))
	for _, app := range apps {
		for _, code := range ErrorCodes {
			keys = append(keys, appCode{app: app, code: code})
		}
	}

	return keys
}

func providerKindKeys(providers []string) []providerKind {
	keys := make([]providerKind, 0, len(providers)*len(FaultKinds))
	for _, provider := range providers {
		for _, kind := range FaultKinds {
			keys = append(keys, providerKind{provider: provider, kind: kind})
		}
	}

	return keys
}
