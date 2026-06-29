package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const DefaultAgentRoot = "/opt/llmswap"
const DefaultAgentConfigPath = DefaultAgentRoot + "/agent.yaml"
const DefaultModelRoot = DefaultAgentRoot + "/models"
const DefaultLlamaSwapConfig = DefaultAgentRoot + "/llama-swap.yaml"
const DefaultSwapPort = 6006

var ErrHelpRequested = errors.New("help requested")

type AgentRuntimeOptions struct {
	ConfigPath  string
	Args        []string
	TailscaleIP func(context.Context) (string, bool)
	LocalIP     func() (string, error)
}

func LoadAgentRuntime(ctx context.Context, opts AgentRuntimeOptions) (AgentConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetEnvPrefix("LLM_SWAP_AGENT")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()

	configDefault := opts.ConfigPath
	if configDefault == "" {
		configDefault = firstNonEmpty(os.Getenv("LLM_SWAP_AGENT_CONFIG"), DefaultAgentConfigPath)
	}
	flags := newAgentRuntimeFlagSet(configDefault, io.Discard)
	if err := flags.Parse(opts.Args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return AgentConfig{}, ErrHelpRequested
		}
		return AgentConfig{}, err
	}

	for _, binding := range []struct {
		key  string
		flag string
	}{
		{"id", "id"},
		{"tags", "tags"},
		{"model_root", "model-root"},
		{"llama_swap_config", "llama-swap-config"},
		{"llama_swap_service", "llama-swap-service"},
		{"restart_command", "restart-command"},
		{"swap_url", "swap-url"},
		{"llama_swap_url", "llama-swap-url"},
		{"swap_port", "swap-port"},
		{"gateway_url", "gateway-url"},
		{"token", "token"},
		{"llama_swap_token", "llama-swap-token"},
	} {
		if err := bindFlag(v, binding.key, flags, binding.flag); err != nil {
			return AgentConfig{}, err
		}
	}
	_ = v.BindEnv("swap_url", "SWAP_URL", "LLM_SWAP_AGENT_SWAP_URL")

	configPath, _ := flags.GetString("config")
	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) && !os.IsNotExist(err) {
				return AgentConfig{}, err
			}
		}
	}

	cfg := AgentConfig{}
	cfg.Agent.ID = configString(v, "id", "")
	cfg.Agent.Tags = configTags(v)
	cfg.Agent.ModelRoot = configString(v, "model_root", DefaultModelRoot)
	cfg.Agent.LlamaSwapConfig = configString(v, "llama_swap_config", DefaultLlamaSwapConfig)
	cfg.Agent.LlamaSwapService = configString(v, "llama_swap_service", "")
	cfg.Agent.RestartCommand = configString(v, "restart_command", "")
	cfg.Agent.SwapURL = firstNonEmpty(configString(v, "swap_url", ""), configString(v, "llama_swap_url", ""))
	cfg.Agent.SwapPort = configInt(v, "swap_port", DefaultSwapPort)
	cfg.Agent.GatewayURL = configString(v, "gateway_url", "")
	cfg.Agent.Token = configString(v, "token", "")
	cfg.Agent.LlamaSwapToken = configString(v, "llama_swap_token", "")
	if cfg.Agent.LlamaSwapToken == "" {
		cfg.Agent.LlamaSwapToken = cfg.Agent.Token
	}

	tailscaleIP := opts.TailscaleIP
	if tailscaleIP == nil {
		tailscaleIP = DefaultTailscaleIP
	}
	localIP := opts.LocalIP
	if localIP == nil {
		localIP = DefaultLocalIP
	}
	swapURL, err := ResolveSwapURL(ctx, cfg.Agent.SwapURL, cfg.Agent.SwapPort, tailscaleIP, localIP)
	if err != nil {
		return cfg, err
	}
	cfg.Agent.SwapURL = swapURL
	cfg.Agent.LlamaSwapURL = swapURL

	if err := validateAgentRuntime(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func AgentRuntimeUsage(opts AgentRuntimeOptions) string {
	configDefault := opts.ConfigPath
	if configDefault == "" {
		configDefault = firstNonEmpty(os.Getenv("LLM_SWAP_AGENT_CONFIG"), DefaultAgentConfigPath)
	}
	var out bytes.Buffer
	flags := newAgentRuntimeFlagSet(configDefault, &out)
	flags.Usage()
	return out.String()
}

func newAgentRuntimeFlagSet(configDefault string, output io.Writer) *pflag.FlagSet {
	flags := pflag.NewFlagSet("agent", pflag.ContinueOnError)
	flags.SetOutput(output)
	flags.String("config", configDefault, "agent config path")
	flags.String("id", "", "agent id")
	flags.StringSlice("tags", nil, "agent tags")
	flags.String("model-root", "", "local model root")
	flags.String("llama-swap-config", "", "rendered llama-swap config path")
	flags.String("llama-swap-service", "", "llama-swap system service")
	flags.String("restart-command", "", "restart shell command")
	flags.String("swap-url", "", "public llama-swap URL advertised to gateway")
	flags.String("llama-swap-url", "", "deprecated alias for swap-url")
	flags.Int("swap-port", 0, "llama-swap port used when swap-url is omitted")
	flags.String("gateway-url", "", "gateway URL")
	flags.String("token", "", "gateway agent token")
	flags.String("llama-swap-token", "", "llama-swap internal token")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage of %s:\n", flags.Name())
		flags.PrintDefaults()
	}
	return flags
}

func ResolveSwapURL(ctx context.Context, explicit string, port int, tailscaleIP func(context.Context) (string, bool), localIP func() (string, error)) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	if port <= 0 {
		port = DefaultSwapPort
	}
	if tailscaleIP != nil {
		if ip, ok := tailscaleIP(ctx); ok && strings.TrimSpace(ip) != "" {
			return "http://" + net.JoinHostPort(strings.TrimSpace(ip), strconv.Itoa(port)), nil
		}
	}
	if localIP == nil {
		return "", fmt.Errorf("local IP resolver is required")
	}
	ip, err := localIP()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(ip) == "" {
		return "", fmt.Errorf("local IP resolver returned empty address")
	}
	return "http://" + net.JoinHostPort(strings.TrimSpace(ip), strconv.Itoa(port)), nil
}

func LocalLlamaSwapURL(port int) string {
	if port <= 0 {
		port = DefaultSwapPort
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

func DefaultTailscaleIP(ctx context.Context) (string, bool) {
	cmd := exec.CommandContext(ctx, "tailscale", "ip", "-4")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, true
		}
	}
	return "", false
}

func DefaultLocalIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			ip = ip.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found")
}

func bindFlag(v *viper.Viper, key string, flags *pflag.FlagSet, flagName string) error {
	flag := flags.Lookup(flagName)
	if flag == nil {
		return fmt.Errorf("unknown flag %q", flagName)
	}
	return v.BindPFlag(key, flag)
}

func configString(v *viper.Viper, key string, fallback string) string {
	if value := strings.TrimSpace(v.GetString(key)); value != "" {
		return value
	}
	if value := strings.TrimSpace(v.GetString("agent." + key)); value != "" {
		return value
	}
	return fallback
}

func configInt(v *viper.Viper, key string, fallback int) int {
	if value := v.GetInt(key); value != 0 {
		return value
	}
	if value := v.GetInt("agent." + key); value != 0 {
		return value
	}
	return fallback
}

func configTags(v *viper.Viper) []string {
	if tags := normalizeTags(v.GetStringSlice("tags")); len(tags) > 0 {
		return tags
	}
	if tags := normalizeTags([]string{v.GetString("tags")}); len(tags) > 0 {
		return tags
	}
	if tags := normalizeTags(v.GetStringSlice("agent.tags")); len(tags) > 0 {
		return tags
	}
	return nil
}

func normalizeTags(values []string) []string {
	var out []string
	for _, value := range values {
		for _, tag := range strings.Split(value, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				out = append(out, tag)
			}
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validateAgentRuntime(cfg AgentConfig) error {
	if cfg.Agent.ID == "" {
		return fmt.Errorf("agent.id is required")
	}
	if len(cfg.Agent.Tags) == 0 {
		return fmt.Errorf("agent.tags is required")
	}
	if cfg.Agent.ModelRoot == "" || cfg.Agent.LlamaSwapConfig == "" || cfg.Agent.LlamaSwapURL == "" || cfg.Agent.GatewayURL == "" {
		return fmt.Errorf("agent model_root, llama_swap_config, swap_url, and gateway_url are required")
	}
	if cfg.Agent.Token == "" {
		return fmt.Errorf("agent.token is required")
	}
	return nil
}
