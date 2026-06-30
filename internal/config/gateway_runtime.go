package config

import (
	"context"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const DefaultGatewayConfigPath = DefaultAgentRoot + "/gateway.yaml"
const DefaultGatewayListenAddr = ":8080"
const DefaultProxyAttempts = 3

type GatewayRuntimeOptions struct {
	ConfigPath string
	Args       []string
}

type GatewayRuntimeConfig struct {
	Config     GatewayConfig
	ConfigPath string
	ListenAddr string
	Overrides  GatewayRuntimeOverrides
}

type GatewayRuntimeOverrides struct {
	ListenAddr    bool
	ProxyAttempts bool
	Tokens        bool
}

func LoadGatewayRuntime(ctx context.Context, opts GatewayRuntimeOptions) (GatewayRuntimeConfig, error) {
	_ = ctx

	flags := pflag.NewFlagSet("gateway", pflag.ContinueOnError)
	configDefault := opts.ConfigPath
	if configDefault == "" {
		configDefault = firstNonEmpty(os.Getenv("LLMSWAP_GATEWAY_CONFIG"), os.Getenv("LLM_SWAP_GATEWAY_CONFIG"), DefaultGatewayConfigPath)
	}
	flags.String("config", configDefault, "gateway config path")
	flags.String("addr", "", "listen address")
	flags.Int("proxy-attempts", 0, "maximum proxy dispatch attempts")
	if err := flags.Parse(opts.Args); err != nil {
		return GatewayRuntimeConfig{}, err
	}

	configPath, _ := flags.GetString("config")
	file, err := os.Open(configPath)
	if err != nil {
		return GatewayRuntimeConfig{}, err
	}
	defer file.Close()

	cfg, err := LoadGateway(file)
	if err != nil {
		return GatewayRuntimeConfig{}, err
	}

	v := viper.New()
	_ = v.BindEnv("addr", "LLMSWAP_GATEWAY_ADDR", "LLM_SWAP_GATEWAY_ADDR")
	_ = v.BindEnv("proxy_attempts", "LLMSWAP_GATEWAY_PROXY_ATTEMPTS", "LLM_SWAP_GATEWAY_PROXY_ATTEMPTS")
	_ = v.BindEnv("gateway.listen_addr", "LLMSWAP_GATEWAY_LISTEN_ADDR", "LLM_SWAP_GATEWAY_GATEWAY_LISTEN_ADDR")
	_ = v.BindEnv("gateway.proxy_attempts", "LLMSWAP_GATEWAY_PROXY_ATTEMPTS", "LLM_SWAP_GATEWAY_GATEWAY_PROXY_ATTEMPTS")
	_ = v.BindEnv("tokens.client", "LLMSWAP_CLIENT_TOKEN", "LLMSWAP_GATEWAY_TOKENS_CLIENT", "LLM_SWAP_GATEWAY_TOKENS_CLIENT")
	_ = v.BindEnv("tokens.agent", "LLMSWAP_AGENT_TOKEN", "LLMSWAP_GATEWAY_TOKENS_AGENT", "LLM_SWAP_GATEWAY_TOKENS_AGENT")
	_ = v.BindEnv("tokens.llama_swap", "LLMSWAP_LLAMA_SWAP_TOKEN", "LLMSWAP_GATEWAY_TOKENS_LLAMA_SWAP", "LLM_SWAP_GATEWAY_TOKENS_LLAMA_SWAP")

	overrides := GatewayRuntimeOverrides{
		ListenAddr:    v.IsSet("addr") || v.IsSet("gateway.listen_addr"),
		ProxyAttempts: v.IsSet("proxy_attempts") || v.IsSet("gateway.proxy_attempts"),
		Tokens:        v.IsSet("tokens.client") || v.IsSet("tokens.agent") || v.IsSet("tokens.llama_swap"),
	}
	applyGatewayEnv(v, &cfg)
	if flags.Changed("addr") {
		cfg.Gateway.ListenAddr, _ = flags.GetString("addr")
		overrides.ListenAddr = true
	}
	if flags.Changed("proxy-attempts") {
		cfg.Gateway.ProxyAttempts, _ = flags.GetInt("proxy-attempts")
		overrides.ProxyAttempts = true
	}
	applyGatewayDefaults(&cfg)
	if err := validateGateway(cfg); err != nil {
		return GatewayRuntimeConfig{}, err
	}

	return GatewayRuntimeConfig{
		Config:     cfg,
		ConfigPath: configPath,
		ListenAddr: firstNonEmpty(cfg.Gateway.ListenAddr, DefaultGatewayListenAddr),
		Overrides:  overrides,
	}, nil
}

func applyGatewayEnv(v *viper.Viper, cfg *GatewayConfig) {
	llamaSwapInheritedAgentToken := cfg.Tokens.LlamaSwap == "" || cfg.Tokens.LlamaSwap == cfg.Tokens.Agent
	llamaSwapTokenOverridden := false
	if value := strings.TrimSpace(v.GetString("addr")); value != "" {
		cfg.Gateway.ListenAddr = value
	}
	if value := v.GetInt("proxy_attempts"); value != 0 {
		cfg.Gateway.ProxyAttempts = value
	}
	if value := strings.TrimSpace(v.GetString("gateway.listen_addr")); value != "" {
		cfg.Gateway.ListenAddr = value
	}
	if value := v.GetInt("gateway.proxy_attempts"); value != 0 {
		cfg.Gateway.ProxyAttempts = value
	}
	if value := strings.TrimSpace(v.GetString("tokens.client")); value != "" {
		cfg.Tokens.Client = value
	}
	if value := strings.TrimSpace(v.GetString("tokens.agent")); value != "" {
		cfg.Tokens.Agent = value
	}
	if value := strings.TrimSpace(v.GetString("tokens.llama_swap")); value != "" {
		cfg.Tokens.LlamaSwap = value
		llamaSwapTokenOverridden = true
	}
	if llamaSwapInheritedAgentToken && !llamaSwapTokenOverridden {
		cfg.Tokens.LlamaSwap = cfg.Tokens.Agent
	}
}
