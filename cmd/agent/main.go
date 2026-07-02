package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"llm-swap/internal/agent"
	"llm-swap/internal/config"
)

func main() {
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
	llamaSwapState := llamaSwapStateClient(cfg, gatewayHTTP)

	reconciler := &agent.Reconciler{
		AgentID:         cfg.Agent.ID,
		Tags:            cfg.Agent.Tags,
		ModelRoot:       cfg.Agent.ModelRoot,
		LlamaSwapConfig: cfg.Agent.LlamaSwapConfig,
		LlamaSwapURL:    cfg.Agent.LlamaSwapURL,
		LlamaSwapToken:  cfg.Agent.LlamaSwapToken,
		Gateway: agent.ConfigClient{
			BaseURL: cfg.Agent.GatewayURL,
			Token:   cfg.Agent.Token,
			HTTP:    gatewayHTTP,
		},
		HTTPClient:    artifactHTTP,
		Service:       service,
		Health:        llamaSwapState,
		RunningModels: llamaSwapState,
		GPUDevices:    agent.NvidiaSMIGPUDevicesClient{},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("agent reconcile loop starting for %s advertised_swap_url=%s local_swap_url=%s", cfg.Agent.ID, cfg.Agent.LlamaSwapURL, llamaSwapState.BaseURL)
	if err := reconciler.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
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
