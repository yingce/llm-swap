package agent

import (
	"context"
	"log"
	"os/exec"
)

type Service interface {
	Restart(context.Context) error
}

type SystemdService struct {
	Name string
}

func (s SystemdService) Restart(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "restart", s.Name).Run()
}

type LoggingService struct {
	Logger *log.Logger
}

func (s LoggingService) Restart(context.Context) error {
	logger := s.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Println("restart requested but no llama_swap_service configured; skipping")
	return nil
}

type FakeService struct {
	Restarts int
	Err      error
}

func (s *FakeService) Restart(context.Context) error {
	s.Restarts++
	return s.Err
}
