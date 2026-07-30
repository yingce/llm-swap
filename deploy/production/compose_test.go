package production

import (
	"os"
	"strings"
	"testing"

	"llm-swap/internal/config"
)

func TestGatewayComposeMountsConfigAndTransportStateDirectories(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compose := string(raw)
	for _, want := range []string{
		"LLMSWAP_GATEWAY_CONFIG: /opt/llmswap/config/gateway.yaml",
		"/opt/llmswap/config:/opt/llmswap/config",
		"/opt/llmswap/state:/opt/llmswap/state",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("production compose missing %q", want)
		}
	}
	if strings.Contains(compose, "/opt/llmswap/gateway.yaml:/opt/llmswap/gateway.yaml") {
		t.Fatal("production compose must not bind-mount gateway.yaml as a single file because atomic replace cannot update it")
	}
}

func TestGatewayProductionExampleUsesDurableLeaseStoreAndLoopbackDial(t *testing.T) {
	raw, err := os.ReadFile("gateway.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	rawConfig := string(raw)
	for _, want := range []string{
		"dial_addr: 127.0.0.1",
		"lease_store_path: /opt/llmswap/state/transport-leases.json",
	} {
		if !strings.Contains(rawConfig, want) {
			t.Fatalf("production gateway example missing %q", want)
		}
	}
	cfg, err := config.LoadGateway(strings.NewReader(rawConfig))
	if err != nil {
		t.Fatalf("production gateway example must load: %v", err)
	}
	if cfg.Transport.FRP.DialAddr != "127.0.0.1" || cfg.Transport.FRP.LeaseStorePath != "/opt/llmswap/state/transport-leases.json" {
		t.Fatalf("unexpected production FRP paths: %+v", cfg.Transport.FRP)
	}
}
