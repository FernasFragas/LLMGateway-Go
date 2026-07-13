package gateway

import "sort"

// planRoutes turns the routes table, the gateway's failover order, and one
// app's policy into the ordered routes this request may use.
//
// Routes serving the requested model always come first, in table order —
// route failover is a transport fact, allowed under every policy. What may
// follow them is the app's judgment, not the gateway's:
//
//   - same-model: nothing follows.
//   - allowlist:  the app's named substitutes, in the app's preference order.
//   - any:        every other route, in the gateway's failover order.
//
// If no route serves the requested model at all, nothing is eligible: there
// is no primary to fail over from, and inventing one would be the gateway
// judging models interchangeable (non-goal).
//
// constrained reports whether the policy excluded routes that do exist — the
// signal that an exhausted plan is the app's recorded fidelity trade (503
// model_unavailable) rather than a gateway failure (502 upstream_failed).
func planRoutes(requested string, policy FailoverPolicy, routes []Route, failoverOrder []string) (eligible []Route, constrained bool) {
	var primary, rest []Route
	for _, r := range routes {
		if r.Model == requested {
			primary = append(primary, r)
		} else {
			rest = append(rest, r)
		}
	}

	if len(primary) == 0 {
		return nil, len(routes) > 0
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
