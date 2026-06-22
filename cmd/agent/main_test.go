package main

import (
	"io"
	"log"
	"testing"

	"llm-swap/internal/agent"
	"llm-swap/internal/config"
)

func TestRestartServicePrefersShellCommand(t *testing.T) {
	var cfg config.AgentConfig
	cfg.Agent.RestartCommand = "docker restart llama-swap"
	cfg.Agent.LlamaSwapService = "llama-swap"

	service := restartService(cfg, log.New(io.Discard, "", 0))

	shell, ok := service.(agent.ShellCommandService)
	if !ok {
		t.Fatalf("restartService returned %T, want agent.ShellCommandService", service)
	}
	if shell.Command != "docker restart llama-swap" {
		t.Fatalf("command = %q, want docker restart llama-swap", shell.Command)
	}
}

func TestRestartServiceUsesSystemdWhenOnlyServiceConfigured(t *testing.T) {
	var cfg config.AgentConfig
	cfg.Agent.LlamaSwapService = "llama-swap"

	service := restartService(cfg, log.New(io.Discard, "", 0))

	systemd, ok := service.(agent.SystemdService)
	if !ok {
		t.Fatalf("restartService returned %T, want agent.SystemdService", service)
	}
	if systemd.Name != "llama-swap" {
		t.Fatalf("name = %q, want llama-swap", systemd.Name)
	}
}

func TestRestartServiceUsesLoggingWhenRestartUnconfigured(t *testing.T) {
	var cfg config.AgentConfig

	service := restartService(cfg, log.New(io.Discard, "", 0))

	if _, ok := service.(agent.LoggingService); !ok {
		t.Fatalf("restartService returned %T, want agent.LoggingService", service)
	}
}

func TestLlamaSwapStateClientUsesLocalSwapPort(t *testing.T) {
	cfg := config.AgentConfig{}
	cfg.Agent.LlamaSwapURL = "https://public-worker.example:8443"
	cfg.Agent.SwapPort = 6006
	cfg.Agent.LlamaSwapToken = "worker-token"

	client := llamaSwapStateClient(cfg, nil)
	if client.BaseURL != "http://127.0.0.1:6006" {
		t.Fatalf("state client base url = %q, want local swap port", client.BaseURL)
	}
	if client.BearerToken != "worker-token" {
		t.Fatalf("state client bearer token = %q, want configured token", client.BearerToken)
	}
}
