package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestShellCommandServiceRestartRunsCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "restart-marker")
	svc := ShellCommandService{Command: fmt.Sprintf("printf restarted > %s", strconv.Quote(marker))}

	if err := svc.Restart(context.Background()); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker was not written: %v", err)
	}
	if string(got) != "restarted" {
		t.Fatalf("marker = %q, want restarted", got)
	}
}

func TestShellCommandServiceRestartReturnsCommandFailure(t *testing.T) {
	svc := ShellCommandService{Command: "exit 23"}

	err := svc.Restart(context.Background())
	if err == nil {
		t.Fatal("Restart returned nil, want command failure")
	}
}
