package gateway

import "testing"

func TestAccountingReleasesOnce(t *testing.T) {
	a := NewAccounting()

	release := a.Acquire("req-1", "qwen", "gpu-4090", "gpu-01")
	release()
	release()

	if got := a.WorkerActive("gpu-01"); got != 0 {
		t.Fatalf("worker active = %d, want 0", got)
	}
	if got := a.ModelActive("qwen"); got != 0 {
		t.Fatalf("model active = %d, want 0", got)
	}
	if got := a.TagActive("gpu-4090"); got != 0 {
		t.Fatalf("tag active = %d, want 0", got)
	}
}

func TestAccountingTracksActiveCountsAndSnapshot(t *testing.T) {
	a := NewAccounting()

	release1 := a.Acquire("req-1", "qwen", "gpu-4090", "gpu-01")
	release2 := a.Acquire("req-2", "qwen", "gpu-4090", "gpu-02")
	release3 := a.Acquire("req-3", "llama", "gpu-a100", "gpu-02")
	defer release1()
	defer release2()
	defer release3()

	if got := a.ModelActive("qwen"); got != 2 {
		t.Fatalf("qwen active = %d, want 2", got)
	}
	if got := a.ModelActive("llama"); got != 1 {
		t.Fatalf("llama active = %d, want 1", got)
	}
	if got := a.TagActive("gpu-4090"); got != 2 {
		t.Fatalf("gpu-4090 active = %d, want 2", got)
	}
	if got := a.WorkerActive("gpu-02"); got != 2 {
		t.Fatalf("gpu-02 active = %d, want 2", got)
	}

	snapshot := a.RequestSnapshot()
	if len(snapshot) != 3 {
		t.Fatalf("snapshot length = %d, want 3", len(snapshot))
	}
	if snapshot["req-1"].Model != "qwen" || snapshot["req-1"].Tag != "gpu-4090" || snapshot["req-1"].WorkerID != "gpu-01" {
		t.Fatalf("snapshot req-1 = %+v", snapshot["req-1"])
	}

	release2()
	if got := a.WorkerActive("gpu-02"); got != 1 {
		t.Fatalf("gpu-02 active after release = %d, want 1", got)
	}
	if _, ok := a.RequestSnapshot()["req-2"]; ok {
		t.Fatal("released request should not remain in snapshot")
	}
}
