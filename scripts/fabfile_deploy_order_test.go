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

func fabfileRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}
