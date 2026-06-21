package main

import (
	"context"
	"flag"
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
	configPath := flag.String("config", "examples/agent.yaml", "agent config path")
	flag.Parse()

	f, err := os.Open(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	cfg, err := config.LoadAgent(f)
	if err != nil {
		log.Fatal(err)
	}

	gatewayHTTP := &http.Client{Timeout: 30 * time.Second}
	artifactHTTP := &http.Client{}
	service := restartService(cfg, log.Default())

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
		HTTPClient: artifactHTTP,
		Service:    service,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("agent reconcile loop starting for %s", cfg.Agent.ID)
	if err := reconciler.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
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
