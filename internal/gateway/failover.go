package gateway

import "sort"

// planModelsProviders turns the model-providers table, the gateway's
// failover order, and one app's policy into the ordered model providers this
// request may use.
//
// Model providers serving the requested model always come first, in table
// order — provider failover is a transport fact, allowed under every policy.
// What may follow them is the app's judgment, not the gateway's:
//
//   - same-model: nothing follows.
//   - allowlist:  the app's named substitutes, in the app's preference order.
//   - any:        every other model provider, in the gateway's failover order.
//
// If no model provider serves the requested model at all, nothing is
// eligible: there is no primary to fail over from, and inventing one would
// be the gateway judging models interchangeable (non-goal).
//
// constrained reports whether the policy excluded model providers that do
// exist — the signal that an exhausted plan is the app's recorded fidelity
// trade (503 model_unavailable) rather than a gateway failure (502
// upstream_failed).
func planModelsProviders(requested string, policy FailoverPolicy, modelProviders []ModelProvider, failoverOrder []string) (eligible []ModelProvider, constrained bool) {
	var primary, rest []ModelProvider
	for _, r := range modelProviders {
		if r.Model == requested {
			primary = append(primary, r)
		} else {
			rest = append(rest, r)
		}
	}

	if len(primary) == 0 {
		return nil, len(modelProviders) > 0
	}
	eligible = primary

	switch policy.Kind {
	case PolicyAny:
		rank := make(map[string]int, len(failoverOrder))
		for i, provider := range failoverOrder {
			rank[provider] = i + 1
		}

		sort.SliceStable(rest, func(i, j int) bool {
			return providerRank(rank, rest[i].Provider) < providerRank(rank, rest[j].Provider)
		})

		return append(eligible, rest...), false
	case PolicyAllowlist:
		allowed := make(map[string]bool, len(policy.Allowlist))
		for _, model := range policy.Allowlist {
			if model == requested {
				continue
			}
			allowed[model] = true
			for _, r := range rest {
				if r.Model == model {
					eligible = append(eligible, r)
				}
			}
		}

		for _, r := range rest {
			if !allowed[r.Model] {
				constrained = true
				break
			}
		}

		return eligible, constrained
	default:
		// PolicySameModel — and the conservative reading of anything
		// unrecognized, including the zero value: substitution is opt-in.
		return eligible, len(rest) > 0
	}
}

func providerRank(rank map[string]int, provider string) int {
	if r, ok := rank[provider]; ok {
		return r
	}

	return len(rank) + 1 // providers absent from the order go last, stably
}
