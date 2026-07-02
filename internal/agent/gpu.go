package agent

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"llm-swap/internal/protocol"
)

type GPUDevicesClient interface {
	GPUDevicesContext(context.Context) ([]protocol.GPUDevice, error)
}

type NvidiaSMIGPUDevicesClient struct {
	Command string
}

func (c NvidiaSMIGPUDevicesClient) GPUDevicesContext(ctx context.Context) ([]protocol.GPUDevice, error) {
	command := c.Command
	if command == "" {
		command = "nvidia-smi"
	}
	cmd := exec.CommandContext(ctx, command,
		"--query-gpu=index,name,uuid,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu",
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, nil
		}
		var execErr *exec.Error
		if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return ParseNvidiaSMIGPUDevices(out)
}

func ParseNvidiaSMIGPUDevices(out []byte) ([]protocol.GPUDevice, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return []protocol.GPUDevice{}, nil
	}
	reader := csv.NewReader(bytes.NewReader(trimmed))
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	devices := make([]protocol.GPUDevice, 0, len(rows))
	for _, row := range rows {
		if len(row) < 8 {
			continue
		}
		devices = append(devices, protocol.GPUDevice{
			Index:              parseInt(row[0]),
			Name:               strings.TrimSpace(row[1]),
			UUID:               strings.TrimSpace(row[2]),
			MemoryTotalMiB:     parseInt64(row[3]),
			MemoryUsedMiB:      parseInt64(row[4]),
			MemoryFreeMiB:      parseInt64(row[5]),
			UtilizationPercent: parseFloat(row[6]),
			TemperatureCelsius: parseFloat(row[7]),
		})
	}
	return devices, nil
}

func parseInt(value string) int {
	return int(parseInt64(value))
}

func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(cleanNvidiaSMINumber(value), 10, 64)
	return parsed
}

func parseFloat(value string) float64 {
	parsed, _ := strconv.ParseFloat(cleanNvidiaSMINumber(value), 64)
	return parsed
}

func cleanNvidiaSMINumber(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "[N/A]") || strings.EqualFold(value, "N/A") {
		return "0"
	}
	return value
}
