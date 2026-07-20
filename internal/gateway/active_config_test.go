package gateway

import (
	"testing"

	"llm-swap/internal/config"
)

func TestActiveGatewayConfigFiltersAliases(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"v1": {},
			"v2": {Disabled: true},
		},
		ModelAliases: map[string]string{
			"stable": "v1",
			"latest": "v2",
		},
	}

	active := activeGatewayConfig(cfg)

	if got := active.ModelAliases["stable"]; got != "v1" {
		t.Fatalf("active stable alias = %q, want v1", got)
	}
	if _, ok := active.ModelAliases["latest"]; ok {
		t.Fatalf("active aliases = %+v, want latest filtered with disabled target", active.ModelAliases)
	}
	if got := cfg.ModelAliases["latest"]; got != "v2" {
		t.Fatalf("source latest alias = %q, want immutable v2", got)
	}
	active.ModelAliases["stable"] = "changed"
	if got := cfg.ModelAliases["stable"]; got != "v1" {
		t.Fatalf("source stable alias = %q after active mutation, want immutable v1", got)
	}
}
