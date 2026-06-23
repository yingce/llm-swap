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
	ListenAddr string
}

func LoadGatewayRuntime(ctx context.Context, opts GatewayRuntimeOptions) (GatewayRuntimeConfig, error) {
	_ = ctx

	flags := pflag.NewFlagSet("gateway", pflag.ContinueOnError)
	configDefault := opts.ConfigPath
	if configDefault == "" {
		configDefault = firstNonEmpty(os.Getenv("LLM_SWAP_GATEWAY_CONFIG"), DefaultGatewayConfigPath)
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
	v.SetEnvPrefix("LLM_SWAP_GATEWAY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	_ = v.BindEnv("addr", "LLM_SWAP_GATEWAY_ADDR")
	_ = v.BindEnv("proxy_attempts", "LLM_SWAP_GATEWAY_PROXY_ATTEMPTS")

	applyGatewayEnv(v, &cfg)
	if flags.Changed("addr") {
		cfg.Gateway.ListenAddr, _ = flags.GetString("addr")
	}
	if flags.Changed("proxy-attempts") {
		cfg.Gateway.ProxyAttempts, _ = flags.GetInt("proxy-attempts")
	}
	applyGatewayDefaults(&cfg)
	if err := validateGateway(cfg); err != nil {
		return GatewayRuntimeConfig{}, err
	}

	return GatewayRuntimeConfig{
		Config:     cfg,
		ListenAddr: firstNonEmpty(cfg.Gateway.ListenAddr, DefaultGatewayListenAddr),
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
