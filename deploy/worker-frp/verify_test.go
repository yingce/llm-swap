package workerfrp

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"llm-swap/internal/config"

	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Networks map[string]struct {
		Driver   string `yaml:"driver"`
		Internal bool   `yaml:"internal"`
	} `yaml:"networks"`
	Secrets map[string]struct {
		File string `yaml:"file"`
	} `yaml:"secrets"`
}

type composeService struct {
	Hostname string `yaml:"hostname"`
	Image    string `yaml:"image"`
	Build    struct {
		Context    string            `yaml:"context"`
		Dockerfile string            `yaml:"dockerfile"`
		Args       map[string]string `yaml:"args"`
	} `yaml:"build"`
	Environment map[string]string `yaml:"environment"`
	Secrets     []struct {
		Source string `yaml:"source"`
		Target string `yaml:"target"`
		Mode   uint32 `yaml:"mode"`
	} `yaml:"secrets"`
	Volumes []struct {
		Type     string `yaml:"type"`
		Source   string `yaml:"source"`
		Target   string `yaml:"target"`
		ReadOnly bool   `yaml:"read_only"`
	} `yaml:"volumes"`
	Networks        []string `yaml:"networks"`
	Ports           []any    `yaml:"ports"`
	Expose          []any    `yaml:"expose"`
	Runtime         string   `yaml:"runtime"`
	Init            bool     `yaml:"init"`
	Restart         string   `yaml:"restart"`
	StopGracePeriod string   `yaml:"stop_grace_period"`
	Deploy          struct {
		Resources struct {
			Reservations struct {
				Devices []struct {
					Driver       string   `yaml:"driver"`
					DeviceIDs    []string `yaml:"device_ids"`
					Capabilities []string `yaml:"capabilities"`
				} `yaml:"devices"`
			} `yaml:"reservations"`
		} `yaml:"resources"`
	} `yaml:"deploy"`
}

func TestComposeDefinesEightIsolatedGPUWorkers(t *testing.T) {
	raw := mustRead(t, "compose.yaml")
	var compose composeFile
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatalf("parse compose.yaml: %v", err)
	}

	wantServices := make([]string, 8)
	for i := range wantServices {
		wantServices[i] = fmt.Sprintf("worker-gpu%d", i)
	}
	gotServices := make([]string, 0, len(compose.Services))
	for name := range compose.Services {
		gotServices = append(gotServices, name)
	}
	sort.Strings(gotServices)
	if !reflect.DeepEqual(gotServices, wantServices) {
		t.Fatalf("services = %v, want %v", gotServices, wantServices)
	}
	if network, ok := compose.Networks["worker-private"]; !ok || network.Driver != "bridge" || network.Internal {
		t.Fatalf("worker-private bridge network missing: %#v", compose.Networks)
	}
	if secret, ok := compose.Secrets["agent_token"]; !ok || strings.TrimSpace(secret.File) == "" {
		t.Fatalf("agent_token file-backed secret missing: %#v", compose.Secrets)
	}

	for i, name := range wantServices {
		svc := compose.Services[name]
		if svc.Hostname != name {
			t.Errorf("%s hostname = %q", name, svc.Hostname)
		}
		if svc.Image == "" || svc.Build.Context != "../.." || svc.Build.Dockerfile != "Dockerfile.agent" {
			t.Errorf("%s must use the shared Dockerfile.agent image/build: %#v", name, svc.Build)
		}
		wantArgs := map[string]string{"LLMSWAP_RUNTIME": "all", "LLMSWAP_INSTALL_TAILSCALE": "0"}
		if !reflect.DeepEqual(svc.Build.Args, wantArgs) {
			t.Errorf("%s build args = %#v, want %#v", name, svc.Build.Args, wantArgs)
		}
		wantEnv := map[string]string{
			"LLMSWAP_GATEWAY_URL":      "${LLMSWAP_GATEWAY_URL:?set LLMSWAP_GATEWAY_URL}",
			"LLMSWAP_AGENT_TOKEN_FILE": "/run/secrets/agent_token",
			"LLMSWAP_AGENT_TAGS":       "gpu-4090",
		}
		if !reflect.DeepEqual(svc.Environment, wantEnv) {
			t.Errorf("%s environment = %#v, want only %#v", name, svc.Environment, wantEnv)
		}
		if len(svc.Secrets) != 1 || svc.Secrets[0].Source != "agent_token" || svc.Secrets[0].Target != "agent_token" || svc.Secrets[0].Mode != 0o400 {
			t.Errorf("%s secret mount = %#v", name, svc.Secrets)
		}
		if !svc.Init || svc.Restart != "unless-stopped" || svc.StopGracePeriod != "45s" {
			t.Errorf("%s lifecycle settings init=%v restart=%q stop_grace_period=%q", name, svc.Init, svc.Restart, svc.StopGracePeriod)
		}
		if len(svc.Ports) != 0 || len(svc.Expose) != 0 || svc.Runtime != "" {
			t.Errorf("%s must not publish/expose ports or set runtime", name)
		}
		if !reflect.DeepEqual(svc.Networks, []string{"worker-private"}) {
			t.Errorf("%s networks = %v", name, svc.Networks)
		}
		devices := svc.Deploy.Resources.Reservations.Devices
		wantDevice := fmt.Sprintf("%d", i)
		if len(devices) != 1 || devices[0].Driver != "nvidia" ||
			!reflect.DeepEqual(devices[0].DeviceIDs, []string{wantDevice}) ||
			!reflect.DeepEqual(devices[0].Capabilities, []string{"gpu"}) {
			t.Errorf("%s device reservation = %#v", name, devices)
		}
		assertWorkerMounts(t, name, svc.Volumes)
	}
}

func assertWorkerMounts(t *testing.T, worker string, mounts []struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
}) {
	t.Helper()
	if len(mounts) != 2 {
		t.Errorf("%s mounts = %#v, want state and shared models", worker, mounts)
		return
	}
	wantLogs := "${WORKER_STATE_ROOT:?set WORKER_STATE_ROOT}/" + worker + "/logs"
	if mounts[0].Type != "bind" || mounts[0].Source != wantLogs || mounts[0].Target != "/opt/llmswap/logs" || mounts[0].ReadOnly {
		t.Errorf("%s logs mount = %#v", worker, mounts[0])
	}
	if mounts[1].Type != "bind" || mounts[1].Source != "${MODEL_ROOT:?set MODEL_ROOT}" || mounts[1].Target != "/opt/llmswap/models" || mounts[1].ReadOnly {
		t.Errorf("%s models mount = %#v", worker, mounts[1])
	}
	for _, mount := range mounts {
		if mount.Target == "/opt/llmswap" {
			t.Errorf("%s must not hide image runtime with a root mount", worker)
		}
	}
}

func TestComposeContainsNoLegacyOrPlaintextTransportConfiguration(t *testing.T) {
	raw := strings.ToLower(string(mustRead(t, "compose.yaml")))
	for _, forbidden := range []string{
		"nvidia_visible_devices", "llmswap_agent_id", "runtime: nvidia",
		"llmswap_enable_tailscale", "tailscale_authkey", "llmswap_swap_url",
		"llmswap_llama_swap_token", "frp_server", "frp_token", "frp_addr",
		"server_addr", "server_port", "auth_token", "remote_port", "port_start",
		"port_end", "frpc:",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("compose.yaml contains forbidden setting %q", forbidden)
		}
	}
	if regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`).MatchString(raw) {
		t.Error("compose.yaml must not contain a literal IPv4 deployment address")
	}
}

func TestGatewayTestTemplateLoads(t *testing.T) {
	raw := mustRead(t, "gateway.test.yaml.example")
	cfg, err := config.LoadGateway(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway(gateway.test.yaml.example): %v", err)
	}
	if cfg.Transport.Type != "frp_tcp" || cfg.Transport.FRP.ServerAddr != "frps.example.test" ||
		cfg.Transport.FRP.ServerPort != 7000 || cfg.Transport.FRP.AuthToken != "replace-me" ||
		cfg.Transport.FRP.PortStart != 2000 || cfg.Transport.FRP.PortEnd != 2007 ||
		cfg.Transport.FRP.LeaseTTLSeconds != 180 {
		t.Fatalf("unexpected FRP test config: %#v", cfg.Transport)
	}
	policy, ok := cfg.TagPolicies["gpu-4090"]
	if !ok || len(policy.AllowedModels) != 1 || policy.AllowedModels[0] != "replace-with-authorized-model" {
		t.Fatalf("gpu-4090 policy = %#v", policy)
	}
	model := cfg.Models["replace-with-authorized-model"]
	if !model.Disabled || model.MinLoaded != 0 {
		t.Fatalf("placeholder model must be disabled and cold: %#v", model)
	}
	if regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`).Match(raw) {
		t.Error("gateway test template must use documentation hostnames, not literal IPv4 deployment addresses")
	}
}

func TestEnvExampleContainsPathsAndNoCredentials(t *testing.T) {
	raw := string(mustRead(t, ".env.example"))
	for _, key := range []string{"WORKER_IMAGE=", "LLMSWAP_GATEWAY_URL=", "WORKER_STATE_ROOT=", "MODEL_ROOT=", "AGENT_TOKEN_FILE="} {
		if !strings.Contains(raw, key) {
			t.Errorf(".env.example missing %s", key)
		}
	}
	for _, forbidden := range []string{"LLMSWAP_AGENT_TOKEN=", "FRP_", "LLAMA_SWAP_TOKEN"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf(".env.example contains forbidden credential/config %q", forbidden)
		}
	}
	if regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`).MatchString(raw) {
		t.Error(".env.example must use documentation hostnames, not literal IPv4 deployment addresses")
	}
	if !strings.Contains(raw, "WORKER_IMAGE=llmswap-agent:frp-REPLACE_WITH_GIT_SHA") {
		t.Error(".env.example must force an immutable deployment-specific image tag")
	}
}

func TestVerifyDefaultsToDeploymentEnv(t *testing.T) {
	raw := string(mustRead(t, "verify.sh"))
	if !strings.Contains(raw, `env_file="${1:-$script_dir/.env}"`) {
		t.Error("verify.sh must default to .env, never .env.example")
	}
}

func TestRunbookCoversImmutableRollbackAndTokenRotation(t *testing.T) {
	raw := strings.Join(strings.Fields(string(mustRead(t, "README.md"))), " ")
	for _, required := range []string{
		"never rebuild or overwrite an older",
		"docker image inspect \"$OLD_WORKER_IMAGE\"",
		"no dual-token overlap",
		"mv -f -- \"$new_token_file\" \"$AGENT_TOKEN_FILE\"",
		"up -d --force-recreate --no-build",
		"does not replace the required secure host-file mode",
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("README.md missing operational safeguard %q", required)
		}
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}
