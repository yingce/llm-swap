package agent

import "testing"

func TestParseNvidiaSMIGPUDevices(t *testing.T) {
	out := []byte("0, NVIDIA GeForce RTX 4090, GPU-aaaa, 24564, 4096, 20468, 25, 58\n1, NVIDIA A100-SXM4-80GB, GPU-bbbb, 81920, 1024, 80896, 3, 41\n")

	devices, err := ParseNvidiaSMIGPUDevices(out)
	if err != nil {
		t.Fatalf("ParseNvidiaSMIGPUDevices() error = %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %+v, want 2", devices)
	}
	first := devices[0]
	if first.Index != 0 || first.Name != "NVIDIA GeForce RTX 4090" || first.UUID != "GPU-aaaa" {
		t.Fatalf("first identity = %+v, want RTX 4090 GPU-aaaa", first)
	}
	if first.MemoryTotalMiB != 24564 || first.MemoryUsedMiB != 4096 || first.MemoryFreeMiB != 20468 {
		t.Fatalf("first memory = %+v, want total/used/free", first)
	}
	if first.UtilizationPercent != 25 || first.TemperatureCelsius != 58 {
		t.Fatalf("first utilization/temp = %+v, want 25%% 58C", first)
	}
}

func TestParseNvidiaSMIGPUDevicesSkipsUnavailableRows(t *testing.T) {
	out := []byte("0, NVIDIA GeForce RTX 4090, GPU-aaaa, [N/A], [N/A], [N/A], [N/A], [N/A]\n")

	devices, err := ParseNvidiaSMIGPUDevices(out)
	if err != nil {
		t.Fatalf("ParseNvidiaSMIGPUDevices() error = %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("devices = %+v, want 1", devices)
	}
	if devices[0].MemoryTotalMiB != 0 || devices[0].UtilizationPercent != 0 {
		t.Fatalf("unavailable numeric fields = %+v, want zero values", devices[0])
	}
}
