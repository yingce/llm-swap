package agent

import (
	"context"
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

type FakeService struct {
	Restarts int
	Err      error
}

func (s *FakeService) Restart(context.Context) error {
	s.Restarts++
	return s.Err
}
