package gateway

// planModelsProviders is the substitution contract as a pure function; each
// case is one clause of decision #5.

import (
	"reflect"
	"testing"
)

var briefModelProviders = []ModelProvider{gptOpenAI, claude, llama}
var briefOrder = []string{"openai", "anthropic", "ollama"}

func TestPlanModelsProviders(t *testing.T) {
	tests := []struct {
		name            string
		requested       string
		policy          FailoverPolicy
		modelProviders  []ModelProvider
		wantEligible    []ModelProvider
		wantConstrained bool
	}{
		{
			name:      "same-model offers only the requested model's providers",
			requested: "gpt-4.1", policy: sameModel(), modelProviders: briefModelProviders,
			wantEligible:    []ModelProvider{gptOpenAI},
			wantConstrained: true, // other model providers exist; the policy refused them
		},
		{
			name:      "the zero-value policy is same-model — substitution is opt-in",
			requested: "gpt-4.1", policy: FailoverPolicy{}, modelProviders: briefModelProviders,
			wantEligible:    []ModelProvider{gptOpenAI},
			wantConstrained: true,
		},
		{
			name:      "same-model still gets provider failover: same weights, second host",
			requested: "gpt-4.1", policy: sameModel(), modelProviders: []ModelProvider{gptOpenAI, gptAzure},
			wantEligible:    []ModelProvider{gptOpenAI, gptAzure},
			wantConstrained: false, // nothing existed that the policy refused
		},
		{
			name:      "any offers everything, substitutes in the gateway's failover order",
			requested: "llama3", policy: anyModel(), modelProviders: briefModelProviders,
			wantEligible:    []ModelProvider{llama, gptOpenAI, claude},
			wantConstrained: false,
		},
		{
			name:      "allowlist substitutes come in the app's preference order",
			requested: "gpt-4.1", policy: allowing("llama3", "claude-sonnet-4"), modelProviders: briefModelProviders,
			wantEligible:    []ModelProvider{gptOpenAI, llama, claude},
			wantConstrained: false, // every existing substitute was named
		},
		{
			name:      "an allowlist naming the requested model does not duplicate its provider",
			requested: "gpt-4.1", policy: allowing("gpt-4.1", "claude-sonnet-4"), modelProviders: briefModelProviders,
			wantEligible:    []ModelProvider{gptOpenAI, claude},
			wantConstrained: true, // llama3 exists and was not named
		},
		{
			name:      "a model no provider serves is eligible nowhere — there is no primary to fail over from",
			requested: "gpt-5", policy: anyModel(), modelProviders: briefModelProviders,
			wantEligible:    nil,
			wantConstrained: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eligible, constrained := buildModelsProvidersFallback(tt.requested, tt.policy, tt.modelProviders, briefOrder)
			if !reflect.DeepEqual(eligible, tt.wantEligible) {
				t.Errorf("eligible = %v, want %v", eligible, tt.wantEligible)
			}
			if constrained != tt.wantConstrained {
				t.Errorf("constrained = %v, want %v", constrained, tt.wantConstrained)
			}
		})
	}
}

func TestPlanModelsProvidersLeavesTheTableUntouched(t *testing.T) {
	table := []ModelProvider{llama, claude, gptOpenAI}
	snapshot := append([]ModelProvider(nil), table...)

	buildModelsProvidersFallback("claude-sonnet-4", anyModel(), table, briefOrder)

	if !reflect.DeepEqual(table, snapshot) {
		t.Errorf("planModelsProviders reordered the shared model-providers table: %v", table)
	}
}
