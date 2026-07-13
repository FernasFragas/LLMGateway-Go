package gateway

// planRoutes is the substitution contract as a pure function; each case is
// one clause of decision #5.

import (
	"reflect"
	"testing"
)

var briefRoutes = []Route{gptOpenAI, claude, llama}
var briefOrder = []string{"openai", "anthropic", "ollama"}

func TestPlanRoutes(t *testing.T) {
	tests := []struct {
		name            string
		requested       string
		policy          FailoverPolicy
		routes          []Route
		wantEligible    []Route
		wantConstrained bool
	}{
		{
			name:      "same-model offers only the requested model's routes",
			requested: "gpt-4.1", policy: sameModel(), routes: briefRoutes,
			wantEligible:    []Route{gptOpenAI},
			wantConstrained: true, // other routes exist; the policy refused them
		},
		{
			name:      "the zero-value policy is same-model — substitution is opt-in",
			requested: "gpt-4.1", policy: FailoverPolicy{}, routes: briefRoutes,
			wantEligible:    []Route{gptOpenAI},
			wantConstrained: true,
		},
		{
			name:      "same-model still gets route failover: same weights, second host",
			requested: "gpt-4.1", policy: sameModel(), routes: []Route{gptOpenAI, gptAzure},
			wantEligible:    []Route{gptOpenAI, gptAzure},
			wantConstrained: false, // nothing existed that the policy refused
		},
		{
			name:      "any offers everything, substitutes in the gateway's failover order",
			requested: "llama3", policy: anyModel(), routes: briefRoutes,
			wantEligible:    []Route{llama, gptOpenAI, claude},
			wantConstrained: false,
		},
		{
			name:      "allowlist substitutes come in the app's preference order",
			requested: "gpt-4.1", policy: allowing("llama3", "claude-sonnet-4"), routes: briefRoutes,
			wantEligible:    []Route{gptOpenAI, llama, claude},
			wantConstrained: false, // every existing substitute was named
		},
		{
			name:      "an allowlist naming the requested model does not duplicate its route",
			requested: "gpt-4.1", policy: allowing("gpt-4.1", "claude-sonnet-4"), routes: briefRoutes,
			wantEligible:    []Route{gptOpenAI, claude},
			wantConstrained: true, // llama3 exists and was not named
		},
		{
			name:      "an unrouted model is eligible nowhere — there is no primary to fail over from",
			requested: "gpt-5", policy: anyModel(), routes: briefRoutes,
			wantEligible:    nil,
			wantConstrained: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eligible, constrained := planRoutes(tt.requested, tt.policy, tt.routes, briefOrder)
			if !reflect.DeepEqual(eligible, tt.wantEligible) {
				t.Errorf("eligible = %v, want %v", eligible, tt.wantEligible)
			}
			if constrained != tt.wantConstrained {
				t.Errorf("constrained = %v, want %v", constrained, tt.wantConstrained)
			}
		})
	}
}

func TestPlanRoutesLeavesTheRoutesTableUntouched(t *testing.T) {
	routes := []Route{llama, claude, gptOpenAI}
	snapshot := append([]Route(nil), routes...)

	planRoutes("claude-sonnet-4", anyModel(), routes, briefOrder)

	if !reflect.DeepEqual(routes, snapshot) {
		t.Errorf("planRoutes reordered the shared routes table: %v", routes)
	}
}
