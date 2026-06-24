package gateway

import (
	"testing"
	"time"
)

func TestReplicaCooldownsMarkClearAndExpire(t *testing.T) {
	now := time.Unix(1000, 0)
	cooldowns := NewReplicaCooldowns(30 * time.Second)

	entry, marked := cooldowns.Mark("worker-a", "qwen", "upstream_503", now)
	if !marked {
		t.Fatal("Mark returned marked=false, want true")
	}
	if entry.WorkerID != "worker-a" || entry.Model != "qwen" || entry.Reason != "upstream_503" {
		t.Fatalf("entry = %+v, want worker/model/reason", entry)
	}
	if got, ok := cooldowns.Get("worker-a", "qwen", now.Add(29*time.Second)); !ok || got.RemainingSeconds != 1 {
		t.Fatalf("Get before expiry = %+v, %v; want active with 1s remaining", got, ok)
	}
	if cooldowns.Active("worker-a", "qwen", now.Add(31*time.Second)) {
		t.Fatal("Active after expiry = true, want false")
	}

	cooldowns.Mark("worker-a", "qwen", "connection_error", now.Add(40*time.Second))
	cleared, ok := cooldowns.Clear("worker-a", "qwen", now.Add(41*time.Second))
	if !ok || cleared.Reason != "connection_error" {
		t.Fatalf("Clear = %+v, %v; want cleared connection_error", cleared, ok)
	}
	if cooldowns.Active("worker-a", "qwen", now.Add(42*time.Second)) {
		t.Fatal("Active after clear = true, want false")
	}
}
