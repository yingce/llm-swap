package agent

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestFRPProxyNameSanitizesAgentIdentityAndIncludesGeneration(t *testing.T) {
	got := frpProxyName(" Worker/GPU 0 + ", 42)
	if !strings.HasPrefix(got, "llmswap-worker-gpu-0-") || !strings.HasSuffix(got, "-g42") {
		t.Fatalf("proxy name = %q", got)
	}
	if second := frpProxyName(" Worker/GPU 0 + ", 42); second != got {
		t.Fatalf("proxy name is not deterministic: %q != %q", second, got)
	}
}

func TestFRPProxyNameRestrictsIdentityToPortableASCII(t *testing.T) {
	got := frpProxyName("节点/GPU①", 3)
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(got) {
		t.Fatalf("proxy name is not portable ASCII: %q", got)
	}
}

func TestFRPProxyNameHashPreventsLossySanitizeCollisions(t *testing.T) {
	first := frpProxyName("gpu/0", 7)
	second := frpProxyName("gpu 0", 7)
	if first == second {
		t.Fatalf("lossy identities collided: %q", first)
	}
	if first != frpProxyName("gpu/0", 7) {
		t.Fatal("proxy name is not deterministic")
	}
	if first == frpProxyName("gpu/0", 8) {
		t.Fatal("proxy name does not include generation")
	}
}

func TestFRPProxyNameIsLengthBoundedForLongAgentIdentity(t *testing.T) {
	got := frpProxyName(strings.Repeat("worker", 100), ^uint64(0))
	if len(got) > 64 {
		t.Fatalf("proxy name length = %d, want <= 64: %q", len(got), got)
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
