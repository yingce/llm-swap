package agent

import (
	"context"
	"testing"
)

func TestFRPProxyNameSanitizesAgentIdentityAndIncludesGeneration(t *testing.T) {
	got := frpProxyName(" Worker/GPU 0 + ", 42)
	if got != "llmswap-worker-gpu-0-g42" {
		t.Fatalf("proxy name = %q", got)
	}
	if second := frpProxyName(" Worker/GPU 0 + ", 42); second != got {
		t.Fatalf("proxy name is not deterministic: %q != %q", second, got)
	}
}

func TestFRPProxyNameRestrictsIdentityToPortableASCII(t *testing.T) {
	if got := frpProxyName("节点/GPU①", 3); got != "llmswap-gpu-g3" {
		t.Fatalf("proxy name = %q", got)
	}
}

func TestFRPClientBoundaryAcceptsRepositoryOwnedConfig(t *testing.T) {
	var _ FRPClient = (*frpBoundaryProbe)(nil)
	var _ FRPClientFactory = frpFactoryProbe{}

	config := FRPProxyConfig{
		ServerAddr: "frps.example.test",
		ServerPort: 7000,
		AuthToken:  "frp-secret",
		ProxyName:  "llmswap-worker-gpu0-g7",
		LocalAddr:  "127.0.0.1:6006",
		RemotePort: 2003,
	}
	client, err := (frpFactoryProbe{}).New(config)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("factory returned nil client")
	}
}

type frpBoundaryProbe struct{}

func (*frpBoundaryProbe) Run(context.Context) error       { return nil }
func (*frpBoundaryProbe) WaitReady(context.Context) error { return nil }
func (*frpBoundaryProbe) Close() error                    { return nil }

type frpFactoryProbe struct{}

func (frpFactoryProbe) New(FRPProxyConfig) (FRPClient, error) {
	return &frpBoundaryProbe{}, nil
}
