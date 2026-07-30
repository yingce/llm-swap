package gateway

import (
	"testing"

	"llm-swap/internal/config"
)

func TestTransportConfigGenerationUsesOnlyEffectiveBootstrapFields(t *testing.T) {
	base := testFRPGatewayConfig()
	base.Tokens.LlamaSwap = "llama-secret"
	want, err := transportConfigGeneration(base)
	if err != nil {
		t.Fatal(err)
	}
	if want == 0 {
		t.Fatal("generation must never be zero")
	}

	unrelated := cloneGatewayConfig(base)
	unrelated.Models["new-model"] = config.Model{}
	unrelated.ModelAliases = map[string]string{"latest": "new-model"}
	unrelated.TagPolicies["new-tag"] = config.TagPolicy{}
	got, err := transportConfigGeneration(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("unrelated config changed generation: got %d want %d", got, want)
	}

	changes := []struct {
		name   string
		mutate func(*config.GatewayConfig)
	}{
		{name: "type", mutate: func(cfg *config.GatewayConfig) { cfg.Transport.Type = "frp_tcp_v2" }},
		{name: "server address", mutate: func(cfg *config.GatewayConfig) { cfg.Transport.FRP.ServerAddr = "other.example.test" }},
		{name: "server port", mutate: func(cfg *config.GatewayConfig) { cfg.Transport.FRP.ServerPort++ }},
		{name: "auth token", mutate: func(cfg *config.GatewayConfig) { cfg.Transport.FRP.AuthToken = "other-frp-secret" }},
		{name: "port start", mutate: func(cfg *config.GatewayConfig) { cfg.Transport.FRP.PortStart++ }},
		{name: "port end", mutate: func(cfg *config.GatewayConfig) { cfg.Transport.FRP.PortEnd++ }},
		{name: "lease TTL", mutate: func(cfg *config.GatewayConfig) { cfg.Transport.FRP.LeaseTTLSeconds++ }},
		{name: "llama token", mutate: func(cfg *config.GatewayConfig) { cfg.Tokens.LlamaSwap = "other-llama-secret" }},
	}
	for _, tt := range changes {
		t.Run(tt.name, func(t *testing.T) {
			changed := cloneGatewayConfig(base)
			tt.mutate(&changed)
			got, err := transportConfigGeneration(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatalf("generation did not change from %d", want)
			}
		})
	}
}

func TestTransportConfigGenerationUsesEffectiveInheritedLlamaSwapToken(t *testing.T) {
	inherited := testFRPGatewayConfig()
	inherited.Tokens.LlamaSwap = ""
	explicit := cloneGatewayConfig(inherited)
	explicit.Tokens.LlamaSwap = explicit.Tokens.Agent

	a, err := transportConfigGeneration(inherited)
	if err != nil {
		t.Fatal(err)
	}
	b, err := transportConfigGeneration(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("inherited generation=%d explicit generation=%d", a, b)
	}
}
