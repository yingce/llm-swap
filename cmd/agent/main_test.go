package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-swap/internal/agent"
	"llm-swap/internal/buildinfo"
	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestAgentVersionTextIncludesSourceVersionAndProtocol(t *testing.T) {
	text := agentVersionText(buildinfo.Current(protocol.AgentProtocolVersion))
	for _, want := range []string{"agent_version=" + buildinfo.AgentVersion, "agent_protocol=3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("version text %q missing %q", text, want)
		}
	}
}

func TestRunAgentTokenFileHexReportsGenericErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "sensitive-token-name")
	var stdout, stderr bytes.Buffer
	if code := runAgentTokenFileHex([]string{missing}, &stdout, &stderr); code == 0 {
		t.Fatal("missing token file succeeded")
	}
	if stdout.Len() != 0 || stderr.String() != "invalid agent token file\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), missing) {
		t.Fatal("generic token file error leaked path")
	}
}

func TestAgentMainDoesNotStartRemovedWorkerTransport(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, obsolete := range []string{"Tunnel" + "Client", "tunnel" + ".Run", "advertised_swap_url"} {
		if strings.Contains(text, obsolete) {
			t.Fatalf("agent main still contains obsolete transport startup %q", obsolete)
		}
	}
}

type mainTestFRPFactory struct{}

func (mainTestFRPFactory) New(agent.FRPProxyConfig) (agent.FRPClient, error) { return nil, nil }

func TestBuildAgentRuntimeKeepsExplicitLegacyFieldsButWaitsForGatewayMode(t *testing.T) {
	cfg := testAgentConfig()
	cfg.Agent.LlamaSwapURL = "http://legacy-worker:6006"
	cfg.Agent.LlamaSwapToken = "legacy-token"

	runtime := buildAgentRuntime(cfg, &http.Client{}, &http.Client{}, &agent.FakeService{}, mainTestFRPFactory{})
	if runtime.transport == nil || runtime.reconciler.TransportState != runtime.transport {
		t.Fatal("agent must resolve transport mode through the gateway")
	}
	if runtime.reconciler.LlamaSwapURL != cfg.Agent.LlamaSwapURL || runtime.reconciler.LlamaSwapToken != "legacy-token" {
		t.Fatalf("legacy reconciler transport = url %q token %q", runtime.reconciler.LlamaSwapURL, runtime.reconciler.LlamaSwapToken)
	}
	if runtime.llamaSwapState.BaseURL != "http://127.0.0.1:6006" || runtime.llamaSwapState.BearerToken != "" || runtime.llamaSwapState.TokenSource == nil {
		t.Fatalf("legacy llama-swap state client = %+v", runtime.llamaSwapState)
	}
	if token := runtime.llamaSwapState.TokenSource(); token != "" {
		t.Fatalf("unresolved token = %q, want empty", token)
	}
}

func TestBuildAgentRuntimeWiresGatewayManagedFRPWithoutTunnel(t *testing.T) {
	cfg := testAgentConfig()
	runtime := buildAgentRuntime(cfg, &http.Client{}, &http.Client{}, &agent.FakeService{}, mainTestFRPFactory{})
	if runtime.transport == nil {
		t.Fatal("empty advertised URL must start gateway-managed FRP")
	}
	if runtime.reconciler.TransportState != runtime.transport {
		t.Fatal("reconciler must read the transport manager snapshot")
	}
	if runtime.reconciler.LlamaSwapURL != cfg.Agent.LlamaSwapURL || runtime.reconciler.LlamaSwapToken != cfg.Agent.LlamaSwapToken {
		t.Fatal("reconciler must retain legacy fallback until gateway mode resolves")
	}
	if runtime.llamaSwapState.BaseURL != "http://127.0.0.1:6006" || runtime.llamaSwapState.BearerToken != "" || runtime.llamaSwapState.TokenSource == nil {
		t.Fatalf("FRP llama-swap state client = %+v", runtime.llamaSwapState)
	}
	if token := runtime.llamaSwapState.TokenSource(); token != "" {
		t.Fatalf("not-ready dynamic token = %q, want empty", token)
	}
	if runtime.transport.Gateway != runtime.configClient || runtime.reconciler.Gateway != runtime.configClient {
		t.Fatal("transport manager and reconciler must share one config client")
	}
	if runtime.transport.SwapPort != 6006 || runtime.transport.AgentID != "worker-01" {
		t.Fatalf("transport manager = %+v", runtime.transport)
	}
}

func testAgentConfig() config.AgentConfig {
	var cfg config.AgentConfig
	cfg.Agent.ID = "worker-01"
	cfg.Agent.Tags = []string{"gpu-4090"}
	cfg.Agent.ModelRoot = "/opt/llmswap/models"
	cfg.Agent.LlamaSwapConfig = "/opt/llmswap/llama-swap.yaml"
	cfg.Agent.GatewayURL = "https://gateway.example.test"
	cfg.Agent.Token = "agent-token"
	cfg.Agent.SwapPort = 6006
	return cfg
}
