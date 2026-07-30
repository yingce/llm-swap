package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"llm-swap/internal/agent"
	"llm-swap/internal/buildinfo"
	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func main() {
	build := buildinfo.Current(protocol.AgentProtocolVersion)
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Print(agentVersionText(build))
		return
	}

	cfg, err := config.LoadAgentRuntime(context.Background(), config.AgentRuntimeOptions{
		Args: os.Args[1:],
	})
	if err != nil {
		if errors.Is(err, config.ErrHelpRequested) {
			fmt.Print(config.AgentRuntimeUsage(config.AgentRuntimeOptions{}))
			return
		}
		log.Fatal(err)
	}

	gatewayHTTP := &http.Client{Timeout: 30 * time.Second}
	artifactHTTP := &http.Client{}
	service := restartService(cfg, log.Default())
	runtime := buildAgentRuntime(cfg, gatewayHTTP, artifactHTTP, service, agent.NewOfficialFRPClientFactory())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("agent reconcile loop starting for %s local_swap_url=%s %s", cfg.Agent.ID, runtime.llamaSwapState.BaseURL, strings.TrimSpace(agentVersionText(build)))
	go func() {
		if err := runtime.transport.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("agent transport manager stopped")
		}
	}()

	if err := runtime.reconciler.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}

type builtAgentRuntime struct {
	configClient   *agent.ConfigClient
	transport      *agent.TransportManager
	reconciler     *agent.Reconciler
	llamaSwapState agent.LlamaSwapStateClient
}

func buildAgentRuntime(cfg config.AgentConfig, gatewayHTTP, artifactHTTP *http.Client, service agent.Service, factory agent.FRPClientFactory) builtAgentRuntime {
	configClient := &agent.ConfigClient{BaseURL: cfg.Agent.GatewayURL, Token: cfg.Agent.Token, HTTP: gatewayHTTP}
	transport := &agent.TransportManager{
		AgentID: cfg.Agent.ID, Tags: append([]string(nil), cfg.Agent.Tags...), AgentToken: cfg.Agent.Token,
		SwapPort: cfg.Agent.SwapPort, Gateway: configClient, Factory: factory,
		Logf: func(string, ...any) { log.Printf("agent transport unavailable") },
	}
	llamaSwapState := llamaSwapStateClient(cfg, gatewayHTTP)
	llamaSwapState.BearerToken = ""
	llamaSwapState.TokenSource = func() string {
		snapshot := transport.Snapshot()
		switch {
		case !snapshot.ModeResolved:
			return ""
		case !snapshot.Managed:
			return cfg.Agent.LlamaSwapToken
		case snapshot.Ready:
			return snapshot.LlamaSwapToken
		default:
			return ""
		}
	}
	reconciler := &agent.Reconciler{
		AgentID: cfg.Agent.ID, Tags: cfg.Agent.Tags, ModelRoot: cfg.Agent.ModelRoot,
		LlamaSwapConfig: cfg.Agent.LlamaSwapConfig, LlamaSwapURL: cfg.Agent.LlamaSwapURL,
		LlamaSwapToken: cfg.Agent.LlamaSwapToken, TransportState: transport,
		Gateway: configClient, HTTPClient: artifactHTTP, Service: service,
		Health: llamaSwapState, RunningModels: llamaSwapState, GPUDevices: agent.NvidiaSMIGPUDevicesClient{},
	}
	return builtAgentRuntime{configClient: configClient, transport: transport, reconciler: reconciler, llamaSwapState: llamaSwapState}
}

func agentVersionText(build protocol.BuildInfo) string {
	return fmt.Sprintf("agent_version=%s agent_commit=%s agent_protocol=%d build_time=%s\n", build.Version, build.Commit, build.ProtocolVersion, build.BuildTime)
}

func llamaSwapStateClient(cfg config.AgentConfig, httpClient *http.Client) agent.LlamaSwapStateClient {
	return agent.LlamaSwapStateClient{
		BaseURL:     config.LocalLlamaSwapURL(cfg.Agent.SwapPort),
		BearerToken: cfg.Agent.LlamaSwapToken,
		HTTP:        httpClient,
	}
}

func restartService(cfg config.AgentConfig, logger *log.Logger) agent.Service {
	if cfg.Agent.RestartCommand != "" {
		return agent.ShellCommandService{Command: cfg.Agent.RestartCommand}
	}
	if cfg.Agent.LlamaSwapService != "" {
		return agent.SystemdService{Name: cfg.Agent.LlamaSwapService}
	}
	logger.Println("agent.llama_swap_service and agent.restart_command are empty; restart requests will fail until configured")
	return agent.LoggingService{Logger: logger}
}
