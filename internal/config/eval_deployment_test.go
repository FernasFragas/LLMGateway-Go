package config

import "testing"

func TestSingleProviderEvaluationExample(t *testing.T) {
	cfg, err := Load("../../deploy/single-provider/config.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Gateway.ModelProviders) != 1 || len(cfg.SecretSource.Providers) != 1 {
		t.Fatal("evaluation deployment must have exactly one route and one credential")
	}
	if len(cfg.Auth.Apps) != 1 {
		t.Fatal("expected one evaluation caller")
	}
}
