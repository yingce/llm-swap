package scripts_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFabricDeployPreparesImagesBeforeGatewayCutover(t *testing.T) {
	fabfile := filepath.Join(fabfileRepoRoot(t), "scripts", "fabfile.py")
	data, err := os.ReadFile(fabfile)
	if err != nil {
		t.Fatalf("read fabfile: %v", err)
	}
	text := string(data)

	buildIdx := strings.Index(text, `docker build -t "$IMAGE" "$GATEWAY_CONTEXT"`)
	pullIdx := strings.Index(text, `docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" pull`)
	cutoverIdx := strings.Index(text, `docker rename "$CONTAINER" "$CONTAINER.previous"`)
	if buildIdx < 0 {
		t.Fatal("deploy script does not build gateway image")
	}
	if pullIdx < 0 {
		t.Fatal("deploy script does not pull compose service images before cutover")
	}
	if cutoverIdx < 0 {
		t.Fatal("deploy script does not contain gateway cutover rename")
	}
	if buildIdx > cutoverIdx {
		t.Fatalf("gateway image build occurs after cutover: build=%d cutover=%d", buildIdx, cutoverIdx)
	}
	if pullIdx > cutoverIdx {
		t.Fatalf("compose image pull occurs after cutover: pull=%d cutover=%d", pullIdx, cutoverIdx)
	}
}

func TestFabricDeployPackagesInstallWorkerScript(t *testing.T) {
	fabfile := filepath.Join(fabfileRepoRoot(t), "scripts", "fabfile.py")
	data, err := os.ReadFile(fabfile)
	if err != nil {
		t.Fatalf("read fabfile: %v", err)
	}
	text := string(data)

	for _, want := range []string{
		`install -m 0644 "$RELEASE_DIR/scripts/install-worker.sh" "$GATEWAY_CONTEXT/install-worker.sh"`,
		`COPY install-worker.sh /usr/local/share/llmswap/install-worker.sh`,
		`chmod 0644 /usr/local/share/llmswap/install-worker.sh`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("deploy script missing %q", want)
		}
	}
}

func TestFabricDeployDoesNotReferenceTailscale(t *testing.T) {
	fabfile := filepath.Join(fabfileRepoRoot(t), "scripts", "fabfile.py")
	data, err := os.ReadFile(fabfile)
	if err != nil {
		t.Fatalf("read fabfile: %v", err)
	}
	if strings.Contains(strings.ToLower(string(data)), "tailscale") {
		t.Fatal("legacy gateway deployment must not reference tailscale")
	}
}

func TestFabricGatewayBuildKeepsCommitAndBuildTimeWithoutInjectingAgentReleaseVersion(t *testing.T) {
	fabfile := filepath.Join(fabfileRepoRoot(t), "scripts", "fabfile.py")
	data, err := os.ReadFile(fabfile)
	if err != nil {
		t.Fatalf("read fabfile: %v", err)
	}
	text := string(data)

	for _, forbidden := range []string{"LLMSWAP_BUILD_VERSION", "internal/buildinfo.Version="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Gateway deploy build must not inject Agent release identity %q", forbidden)
		}
	}
	for _, required := range []string{
		`-e LLMSWAP_BUILD_COMMIT="$COMMIT"`,
		`-e LLMSWAP_BUILD_TIME="$BUILD_TIME"`,
		`internal/buildinfo.Commit=$LLMSWAP_BUILD_COMMIT`,
		`internal/buildinfo.BuildTime=$LLMSWAP_BUILD_TIME`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Gateway deploy build missing independent provenance injection %q", required)
		}
	}
}

func fabfileRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}
