package gateway

// PolicyKind is the shape of an app's substitution contract.
type PolicyKind string

const (
	// PolicySameModel permits provider failover only: the identical model
	// via another model provider. Same weights, second host — a transport
	// fact, always allowed. No substitutes, ever.
	PolicySameModel PolicyKind = "same-model"
	// PolicyAllowlist permits the substitutes the app itself named, tried in
	// the app's preference order.
	PolicyAllowlist PolicyKind = "allowlist"
	// PolicyAny takes whatever the gateway's failover order offers —
	// availability over fidelity.
	PolicyAny PolicyKind = "any"
)

// FailoverPolicy is an app's substitution contract: the semantic judgment of
// which models may stand in for the one it asked for. It belongs to the app
// alone — the gateway never makes that judgment (decision #5). The zero
// value behaves as same-model: substitution is opt-in.
type FailoverPolicy struct {
	Kind      PolicyKind
	Allowlist []string // Kind == PolicyAllowlist only: acceptable substitutes, preference order
}
