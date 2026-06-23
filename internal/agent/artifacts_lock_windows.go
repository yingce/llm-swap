//go:build windows

package agent

import (
	"context"
	"os"
)

func lockArtifactFile(file *os.File) error {
	return nil
}

func lockArtifactFileContext(ctx context.Context, file *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func unlockArtifactFile(file *os.File) error {
	return nil
}
