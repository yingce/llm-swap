package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	ConfigPath string
	Args       []string
	Root       string
	Identity   AgentIdentityProvider
	// TailscaleIP and LocalIP are retained for callers of ResolveSwapURL. Agent
	// runtime loading no longer derives an advertised worker URL from either.
	TailscaleIP func(context.Context) (string, bool)
	LocalIP     func() (string, error)
}

type AgentIdentityProvider interface {
	Hostname() (string, error)
	NewID() (string, error)
}

type defaultAgentIdentityProvider struct{}

func (defaultAgentIdentityProvider) Hostname() (string, error) {
	return os.Hostname()
}

func (defaultAgentIdentityProvider) NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return formatUUID(value[:]), nil
}

func LoadAgentRuntime(ctx context.Context, opts AgentRuntimeOptions) (AgentConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	v := viper.New()
	v.SetConfigType("yaml")

	root := firstNonEmpty(opts.Root, os.Getenv("LLMSWAP_ROOT"), DefaultAgentRoot)
	configDefault := opts.ConfigPath
	if configDefault == "" {
		configDefault = firstNonEmpty(os.Getenv("LLMSWAP_AGENT_CONFIG"), filepath.Join(root, "agent.yaml"))
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
		envAliases := append([]string{binding.key}, agentEnvAliases(binding.key)...)
		if err := v.BindEnv(envAliases...); err != nil {
			return AgentConfig{}, err
		}
	}
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
	cfg.Agent.ModelRoot = configString(v, "model_root", filepath.Join(root, "models"))
	cfg.Agent.LlamaSwapConfig = configString(v, "llama_swap_config", filepath.Join(root, "llama-swap.yaml"))
	cfg.Agent.LlamaSwapService = configString(v, "llama_swap_service", "")
	cfg.Agent.RestartCommand = configString(v, "restart_command", "")
	cfg.Agent.SwapURL = firstNonEmpty(configString(v, "swap_url", ""), configString(v, "llama_swap_url", ""))
	cfg.Agent.SwapPort = configInt(v, "swap_port", DefaultSwapPort)
	cfg.Agent.GatewayURL = configString(v, "gateway_url", "")
	cfg.Agent.Token = configString(v, "token", "")
	cfg.Agent.LlamaSwapToken = configString(v, "llama_swap_token", "")

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
	if cfg.Agent.LlamaSwapToken == "" {
		cfg.Agent.LlamaSwapToken = cfg.Agent.Token
	}

	identity := opts.Identity
	if identity == nil {
		identity = defaultAgentIdentityProvider{}
	}
	agentID, err := resolveAgentID(root, cfg.Agent.ID, identity)
	if err != nil {
		return cfg, err
	}
	cfg.Agent.ID = agentID

	if err := validateAgentRuntime(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func resolveAgentID(root, configuredID string, provider AgentIdentityProvider) (string, error) {
	if id := strings.TrimSpace(configuredID); id != "" {
		return id, nil
	}

	path := filepath.Join(root, "agent-id")
	persisted, err := readPersistedAgentID(path)
	if err != nil {
		return "", err
	}
	if persisted.Valid {
		if err := os.Chmod(path, 0o600); err != nil {
			return "", err
		}
		return persisted.ID, nil
	}
	if persisted.Exists {
		return recoverInvalidPersistedAgentID(path, persisted.Raw)
	}
	if hostname, err := provider.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		return strings.TrimSpace(hostname), nil
	}

	id, err := provider.NewID()
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if !validAgentID(id) {
		return "", fmt.Errorf("agent identity provider generated an invalid ID")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return publishNewAgentID(path, id)
}

type persistedAgentID struct {
	ID     string
	Raw    string
	Exists bool
	Valid  bool
}

func readPersistedAgentID(path string) (persistedAgentID, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return persistedAgentID{}, nil
	}
	if err != nil {
		return persistedAgentID{}, err
	}
	id := strings.TrimSpace(string(contents))
	return persistedAgentID{ID: id, Raw: string(contents), Exists: true, Valid: validAgentID(id)}, nil
}

func waitForPersistedAgentID(path string) (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		persisted, err := readPersistedAgentID(path)
		if err != nil {
			return "", err
		}
		if persisted.Valid {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", err
			}
			return persisted.ID, nil
		}
		time.Sleep(time.Millisecond)
	}
	return "", fmt.Errorf("persisted agent identity was not initialized")
}

func publishNewAgentID(path, id string) (string, error) {
	tempPath, err := writeAgentIDTemp(path, id)
	if err != nil {
		return "", err
	}
	defer os.Remove(tempPath)
	if err := os.Link(tempPath, path); err != nil {
		if os.IsExist(err) {
			return waitForPersistedAgentID(path)
		}
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func recoverInvalidPersistedAgentID(path, raw string) (string, error) {
	id := deterministicRecoveredAgentID(path, raw)
	tempPath, err := writeAgentIDTemp(path, id)
	if err != nil {
		return "", err
	}
	defer os.Remove(tempPath)
	if err := os.Rename(tempPath, path); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func writeAgentIDTemp(path, id string) (string, error) {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if _, err := io.WriteString(temp, id+"\n"); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return tempPath, nil
}

func deterministicRecoveredAgentID(path, raw string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path) + "\n" + raw))
	value := sum[:16]
	value[6] = value[6]&0x0f | 0x50
	value[8] = value[8]&0x3f | 0x80
	return formatUUID(value)
}

func formatUUID(value []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func validAgentID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
			return false
		}
	}
	return true
}

func AgentRuntimeUsage(opts AgentRuntimeOptions) string {
	root := firstNonEmpty(opts.Root, os.Getenv("LLMSWAP_ROOT"), DefaultAgentRoot)
	configDefault := opts.ConfigPath
	if configDefault == "" {
		configDefault = firstNonEmpty(os.Getenv("LLMSWAP_AGENT_CONFIG"), filepath.Join(root, "agent.yaml"))
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
	flags.String("swap-url", "", "legacy public llama-swap URL advertised to gateway")
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

func agentEnvAliases(key string) []string {
	switch key {
	case "id":
		return []string{"LLMSWAP_AGENT_ID"}
	case "tags":
		return []string{"LLMSWAP_AGENT_TAGS"}
	case "model_root":
		return []string{"LLMSWAP_MODEL_ROOT"}
	case "llama_swap_config":
		return []string{"LLMSWAP_LLAMA_SWAP_CONFIG"}
	case "llama_swap_service":
		return []string{"LLMSWAP_LLAMA_SWAP_SERVICE"}
	case "restart_command":
		return []string{"LLMSWAP_AGENT_RESTART_COMMAND"}
	case "swap_url":
		return []string{"LLMSWAP_SWAP_URL"}
	case "llama_swap_url":
		return []string{"LLMSWAP_LLAMA_SWAP_URL"}
	case "swap_port":
		return []string{"LLMSWAP_SWAP_PORT"}
	case "gateway_url":
		return []string{"LLMSWAP_GATEWAY_URL"}
	case "token":
		return []string{"LLMSWAP_AGENT_TOKEN"}
	case "llama_swap_token":
		return []string{"LLMSWAP_LLAMA_SWAP_TOKEN"}
	default:
		return nil
	}
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
	if cfg.Agent.ModelRoot == "" || cfg.Agent.LlamaSwapConfig == "" || cfg.Agent.GatewayURL == "" {
		return fmt.Errorf("agent model_root, llama_swap_config, and gateway_url are required")
	}
	if cfg.Agent.Token == "" {
		return fmt.Errorf("agent.token is required")
	}
	return nil
}
