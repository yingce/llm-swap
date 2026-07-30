package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTransportLeaseAcquireIsStickyAndRenews(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ids := leaseIDSequence("lease-a", "lease-b")
	manager, err := NewTransportLeaseManager(NewMemoryTransportLeaseStore(), clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2007, TTL: 3 * time.Minute}

	first, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseID != "lease-a" || first.Slot != 0 || first.RemotePort != 2000 {
		t.Fatalf("first lease = %#v", first)
	}
	if want := now.Add(policy.TTL); !first.ExpiresAt.Equal(want) {
		t.Fatalf("first expiry = %v, want %v", first.ExpiresAt, want)
	}

	now = now.Add(time.Minute)
	renewed, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.LeaseID != first.LeaseID || renewed.RemotePort != first.RemotePort {
		t.Fatalf("renewed lease = %#v, want same identity and port as %#v", renewed, first)
	}
	if want := now.Add(policy.TTL); !renewed.ExpiresAt.Equal(want) {
		t.Fatalf("renewed expiry = %v, want %v", renewed.ExpiresAt, want)
	}
}

func TestTransportLeaseLookupCurrentIsReadOnlyAndRejectsExpiredLease(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	store := &countingTransportLeaseStore{TransportLeaseStore: NewMemoryTransportLeaseStore()}
	manager := newTestTransportLeaseManager(t, store, func() time.Time { return now }, leaseIDSequence("lease-a"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
	lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	saves := store.saves
	got, err := manager.LookupCurrent(lease.AgentID, lease.LeaseID, lease.Generation)
	if err != nil || got != lease {
		t.Fatalf("lookup = %+v, %v; want %+v", got, err, lease)
	}
	if store.saves != saves {
		t.Fatalf("lookup saved state: %d -> %d", saves, store.saves)
	}
	now = lease.ExpiresAt
	if _, err := manager.LookupCurrent(lease.AgentID, lease.LeaseID, lease.Generation); !errors.Is(err, ErrTransportLeaseNotFound) {
		t.Fatalf("expired lookup error=%v, want %v", err, ErrTransportLeaseNotFound)
	}
	if store.saves != saves {
		t.Fatalf("expired lookup saved state: %d -> %d", saves, store.saves)
	}
}

func TestTransportLeaseAcquireRejectsMismatchedCurrentLeaseID(t *testing.T) {
	store := &countingTransportLeaseStore{TransportLeaseStore: NewMemoryTransportLeaseStore()}
	manager := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence("lease-a"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
	lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	saves := store.saves

	if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{
		AgentID: "worker-a", Generation: 1, CurrentLeaseID: "wrong",
	}); !errors.Is(err, ErrTransportLeaseConflict) {
		t.Fatalf("mismatched sticky acquire error = %v, want %v", err, ErrTransportLeaseConflict)
	}
	if store.saves != saves {
		t.Fatalf("mismatched sticky acquire saved state: saves %d -> %d", saves, store.saves)
	}
	renewed, err := manager.Acquire(policy, TransportLeaseAcquireRequest{
		AgentID: "worker-a", Generation: 1, CurrentLeaseID: lease.LeaseID,
	})
	if err != nil || renewed.LeaseID != lease.LeaseID {
		t.Fatalf("exact sticky acquire = %#v, %v", renewed, err)
	}
}

func TestTransportLeaseReplacementRequiresExactCurrentLeaseID(t *testing.T) {
	tests := []struct {
		name        string
		replacement func(TransportLease, TransportLeasePolicy) TransportLeaseAcquireRequest
	}{
		{
			name: "excluded slot",
			replacement: func(current TransportLease, _ TransportLeasePolicy) TransportLeaseAcquireRequest {
				return TransportLeaseAcquireRequest{AgentID: current.AgentID, Generation: current.Generation, ExcludedSlots: []int{current.Slot}}
			},
		},
		{
			name: "new generation",
			replacement: func(current TransportLease, _ TransportLeasePolicy) TransportLeaseAcquireRequest {
				return TransportLeaseAcquireRequest{AgentID: current.AgentID, Generation: current.Generation + 1}
			},
		},
		{
			name: "changed policy",
			replacement: func(current TransportLease, _ TransportLeasePolicy) TransportLeaseAcquireRequest {
				return TransportLeaseAcquireRequest{AgentID: current.AgentID, Generation: current.Generation, ExcludedSlots: nil}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &countingTransportLeaseStore{TransportLeaseStore: NewMemoryTransportLeaseStore()}
			manager := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence("old", "new"))
			policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2002, TTL: time.Minute}
			current, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			replacementPolicy := policy
			if tt.name == "changed policy" {
				replacementPolicy = TransportLeasePolicy{PortStart: 2001, PortEnd: 2002, TTL: time.Minute}
			}
			request := tt.replacement(current, replacementPolicy)
			saves := store.saves
			for _, leaseID := range []string{"", "wrong"} {
				request.CurrentLeaseID = leaseID
				if _, err := manager.Acquire(replacementPolicy, request); !errors.Is(err, ErrTransportLeaseConflict) {
					t.Fatalf("replacement with lease id %q error = %v, want %v", leaseID, err, ErrTransportLeaseConflict)
				}
				if store.saves != saves {
					t.Fatalf("rejected replacement saved state: saves %d -> %d", saves, store.saves)
				}
			}
			request.CurrentLeaseID = current.LeaseID
			replacement, err := manager.Acquire(replacementPolicy, request)
			if err != nil {
				t.Fatal(err)
			}
			if replacement.LeaseID == current.LeaseID || replacement.RemotePort == current.RemotePort {
				t.Fatalf("replacement = %#v, current = %#v", replacement, current)
			}
		})
	}
}

func TestTransportLeaseAcquireRejectsStaleIDWithoutCurrentLease(t *testing.T) {
	store := &countingTransportLeaseStore{TransportLeaseStore: NewMemoryTransportLeaseStore()}
	manager := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence("unused"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
	if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{
		AgentID: "worker-a", Generation: 1, CurrentLeaseID: "stale",
	}); !errors.Is(err, ErrTransportLeaseConflict) {
		t.Fatalf("stale acquire error = %v, want %v", err, ErrTransportLeaseConflict)
	}
	if store.saves != 0 {
		t.Fatalf("stale acquire performed %d saves", store.saves)
	}
}

func TestTransportLeaseAcquireRecoversExpiredOwnedLeaseID(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), func() time.Time { return now }, leaseIDSequence("old", "fresh"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
	old, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	now = old.ExpiresAt
	fresh, err := manager.Acquire(policy, TransportLeaseAcquireRequest{
		AgentID: "worker-a", Generation: 2, CurrentLeaseID: old.LeaseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.LeaseID != "fresh" || fresh.Generation != 2 || fresh.RemotePort != old.RemotePort {
		t.Fatalf("fresh lease = %#v, expired = %#v", fresh, old)
	}
}

func TestTransportLeaseExpiredIDRecoveryRejectsUnownedOrObsoleteIDs(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
		store := &countingTransportLeaseStore{TransportLeaseStore: NewMemoryTransportLeaseStore()}
		manager := newTestTransportLeaseManager(t, store, func() time.Time { return now }, leaseIDSequence("old"))
		policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
		old, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		now = old.ExpiresAt
		saves := store.saves
		if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 2, CurrentLeaseID: "unknown"}); !errors.Is(err, ErrTransportLeaseConflict) {
			t.Fatalf("unknown expired id error = %v", err)
		}
		if store.saves != saves {
			t.Fatalf("unknown id saved state: %d -> %d", saves, store.saves)
		}
	})

	t.Run("other agent expired id", func(t *testing.T) {
		now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
		manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), func() time.Time { return now }, leaseIDSequence("old"))
		policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
		old, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		now = old.ExpiresAt
		if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1, CurrentLeaseID: old.LeaseID}); !errors.Is(err, ErrTransportLeaseConflict) {
			t.Fatalf("other-agent expired id error = %v", err)
		}
	})

	t.Run("released id", func(t *testing.T) {
		manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), fixedClock(time.Now()), leaseIDSequence("released"))
		policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
		lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		if released, err := manager.Release(lease.AgentID, lease.LeaseID, lease.Generation); err != nil || !released {
			t.Fatalf("release = %v, %v", released, err)
		}
		if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 2, CurrentLeaseID: lease.LeaseID}); !errors.Is(err, ErrTransportLeaseConflict) {
			t.Fatalf("released id error = %v", err)
		}
	})

	t.Run("expired id cannot bypass newer current", func(t *testing.T) {
		now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
		manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), func() time.Time { return now }, leaseIDSequence("old", "current"))
		policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
		old, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		now = old.ExpiresAt
		current, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 2, CurrentLeaseID: old.LeaseID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 3, CurrentLeaseID: old.LeaseID}); !errors.Is(err, ErrTransportLeaseConflict) {
			t.Fatalf("old id with newer current %#v error = %v", current, err)
		}
	})
}

func TestTransportLeaseIDCollisionRetriesThenUsesUniqueID(t *testing.T) {
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), fixedClock(time.Now()), leaseIDSequence("lease-a", "lease-a", "lease-b"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
	first, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseID != "lease-a" || second.LeaseID != "lease-b" {
		t.Fatalf("leases = %#v, %#v", first, second)
	}
}

func TestTransportLeaseIDRepeatedCollisionRollsBackAndReloads(t *testing.T) {
	ids := []string{"lease-a"}
	for i := 0; i < transportLeaseIDGenerationAttempts; i++ {
		ids = append(ids, "lease-a")
	}
	store := &countingTransportLeaseStore{TransportLeaseStore: NewMemoryTransportLeaseStore()}
	manager := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence(ids...))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
	first, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	saves := store.saves
	if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1}); !errors.Is(err, ErrTransportLeaseIDCollision) {
		t.Fatalf("repeated collision error = %v, want %v", err, ErrTransportLeaseIDCollision)
	}
	if store.saves != saves {
		t.Fatalf("collision failure saved state: %d -> %d", saves, store.saves)
	}

	reloaded := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence("lease-b"))
	sticky, err := reloaded.Acquire(policy, TransportLeaseAcquireRequest{AgentID: first.AgentID, Generation: first.Generation, CurrentLeaseID: first.LeaseID})
	if err != nil || sticky.LeaseID != first.LeaseID {
		t.Fatalf("reloaded sticky lease = %#v, %v", sticky, err)
	}
	second, err := reloaded.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1})
	if err != nil || second.LeaseID != "lease-b" {
		t.Fatalf("post-reload lease = %#v, %v", second, err)
	}
}

func TestTransportLeaseAllocatesLowestLiveUniquePorts(t *testing.T) {
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), fixedClock(time.Now()), leaseIDSequence("a", "b"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}

	first, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Slot != 0 || second.Slot != 1 || first.RemotePort == second.RemotePort {
		t.Fatalf("leases = %#v, %#v", first, second)
	}
	if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-c", Generation: 1}); !errors.Is(err, ErrTransportLeaseCapacity) {
		t.Fatalf("capacity error = %v, want %v", err, ErrTransportLeaseCapacity)
	}
}

func TestTransportLeaseReplacementQuarantinesOldPort(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), func() time.Time { return now }, leaseIDSequence("old", "replacement", "other"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2002, TTL: time.Minute}

	old, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1, CurrentLeaseID: old.LeaseID, ExcludedSlots: []int{old.Slot}})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.RemotePort != 2001 {
		t.Fatalf("replacement port = %d, want 2001", replacement.RemotePort)
	}
	other, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if other.RemotePort != 2002 {
		t.Fatalf("other port = %d, want 2002 while old port is quarantined", other.RemotePort)
	}
	if _, err := manager.Renew(policy, old.AgentID, old.LeaseID, old.Generation); !errors.Is(err, ErrTransportLeaseNotFound) {
		t.Fatalf("superseded renew error = %v, want %v", err, ErrTransportLeaseNotFound)
	}
}

func TestTransportLeaseExpiryAllowsPortReuse(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), func() time.Time { return now }, leaseIDSequence("old", "new"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
	if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if lease.RemotePort != 2000 || lease.LeaseID != "new" {
		t.Fatalf("reused lease = %#v", lease)
	}
}

func TestTransportLeaseGenerationReplacementUsesNewPort(t *testing.T) {
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), fixedClock(time.Now()), leaseIDSequence("g1", "g2"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
	first, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 2, CurrentLeaseID: first.LeaseID})
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != 2 || second.LeaseID == first.LeaseID || second.RemotePort != 2001 {
		t.Fatalf("replacement = %#v, first = %#v", second, first)
	}
}

func TestTransportLeaseExactRenewAndRelease(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), func() time.Time { return now }, leaseIDSequence("lease-a"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
	lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []struct {
		agent, lease string
		generation   uint64
	}{{"worker-b", lease.LeaseID, 1}, {"worker-a", "wrong", 1}, {"worker-a", lease.LeaseID, 2}} {
		if _, err := manager.Renew(policy, mismatch.agent, mismatch.lease, mismatch.generation); !errors.Is(err, ErrTransportLeaseNotFound) {
			t.Fatalf("Renew(%q, %q, %d) error = %v", mismatch.agent, mismatch.lease, mismatch.generation, err)
		}
		if released, err := manager.Release(mismatch.agent, mismatch.lease, mismatch.generation); err != nil || released {
			t.Fatalf("Release(%q, %q, %d) = %v, %v", mismatch.agent, mismatch.lease, mismatch.generation, released, err)
		}
	}
	now = now.Add(30 * time.Second)
	renewed, err := manager.Renew(policy, lease.AgentID, lease.LeaseID, lease.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(now.Add(policy.TTL)) {
		t.Fatalf("expiry = %v", renewed.ExpiresAt)
	}
	if released, err := manager.Release(lease.AgentID, lease.LeaseID, lease.Generation); err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
}

func TestTransportLeaseRenewPersistsOnlyAtHalfTTL(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	store := &countingTransportLeaseStore{TransportLeaseStore: NewMemoryTransportLeaseStore()}
	manager := newTestTransportLeaseManager(t, store, func() time.Time { return now }, leaseIDSequence("lease-a"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
	lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	saves := store.saves

	now = now.Add(10 * time.Second)
	immediate, err := manager.Renew(policy, lease.AgentID, lease.LeaseID, lease.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if immediate != lease {
		t.Fatalf("early renew changed lease: got %+v want %+v", immediate, lease)
	}
	if store.saves != saves {
		t.Fatalf("early renew saves=%d, want %d", store.saves, saves)
	}

	now = lease.RenewedAt.Add(policy.TTL / 2)
	renewed, err := manager.Renew(policy, lease.AgentID, lease.LeaseID, lease.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if store.saves != saves+1 {
		t.Fatalf("threshold renew saves=%d, want %d", store.saves, saves+1)
	}
	if !renewed.RenewedAt.Equal(now) || !renewed.ExpiresAt.Equal(now.Add(policy.TTL)) {
		t.Fatalf("threshold renewed lease=%+v", renewed)
	}
}

func TestTransportLeaseConcurrentAcquireKeepsPortsUnique(t *testing.T) {
	const workers = 32
	ids := make([]string, workers)
	for i := range ids {
		ids[i] = fmt.Sprintf("lease-%d", i)
	}
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), fixedClock(time.Now()), leaseIDSequence(ids...))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000 + workers - 1, TTL: time.Minute}
	results := make(chan TransportLease, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: fmt.Sprintf("worker-%d", index), Generation: 1})
			if err != nil {
				errs <- err
				return
			}
			results <- lease
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ports := map[int]bool{}
	for lease := range results {
		if ports[lease.RemotePort] {
			t.Fatalf("duplicate live port %d", lease.RemotePort)
		}
		ports[lease.RemotePort] = true
	}
	if len(ports) != workers {
		t.Fatalf("got %d ports, want %d", len(ports), workers)
	}
}

func TestTransportLeaseStoreSurvivesReloadAndRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	store := NewFileTransportLeaseStore(path)
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
	firstManager := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence("lease-a"))
	first, err := firstManager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	secondManager := newTestTransportLeaseManager(t, NewFileTransportLeaseStore(path), fixedClock(time.Now()), leaseIDSequence("lease-b"))
	reloaded, err := secondManager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LeaseID != first.LeaseID || reloaded.RemotePort != first.RemotePort {
		t.Fatalf("reloaded = %#v, want %#v", reloaded, first)
	}

	corruptPath := filepath.Join(t.TempDir(), "corrupt.json")
	writeTestFile(t, corruptPath, []byte("not-json"))
	if _, err := NewTransportLeaseManager(NewFileTransportLeaseStore(corruptPath), fixedClock(time.Now()), leaseIDSequence("unused")); err == nil {
		t.Fatal("corrupt store load succeeded")
	}
}

func TestFileTransportLeaseStoreRejectsUnsupportedVersionAndEmptyFile(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content []byte
	}{
		{name: "unsupported version", content: []byte(`{"version":2,"leases":[]}`)},
		{name: "empty file", content: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "leases.json")
			writeTestFile(t, path, tt.content)
			if _, err := NewFileTransportLeaseStore(path).Load(); err == nil {
				t.Fatal("invalid persisted store loaded without error")
			}
		})
	}
}

func TestTransportLeaseSaveFailureRollsBackMemory(t *testing.T) {
	store := &failingTransportLeaseStore{failSave: true}
	manager := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence("lease-a", "lease-b"))
	policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
	if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1}); err == nil {
		t.Fatal("acquire succeeded despite save failure")
	}
	store.failSave = false
	lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if lease.RemotePort != 2000 || lease.LeaseID != "lease-b" {
		t.Fatalf("lease after rollback = %#v", lease)
	}
}

func TestTransportLeaseSaveFailureRollsBackRenewAndRelease(t *testing.T) {
	t.Run("renew", func(t *testing.T) {
		now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
		store := &failingTransportLeaseStore{}
		manager := newTestTransportLeaseManager(t, store, func() time.Time { return now }, leaseIDSequence("lease-a", "lease-b"))
		policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
		lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(30 * time.Second)
		store.failSave = true
		if _, err := manager.Renew(policy, lease.AgentID, lease.LeaseID, lease.Generation); err == nil {
			t.Fatal("renew succeeded despite save failure")
		}
		store.failSave = false
		now = lease.ExpiresAt
		reused, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1})
		if err != nil {
			t.Fatalf("failed renew leaked into memory: %v", err)
		}
		if reused.RemotePort != lease.RemotePort {
			t.Fatalf("reused port = %d, want %d", reused.RemotePort, lease.RemotePort)
		}
	})

	t.Run("release", func(t *testing.T) {
		store := &failingTransportLeaseStore{}
		manager := newTestTransportLeaseManager(t, store, fixedClock(time.Now()), leaseIDSequence("lease-a", "lease-b"))
		policy := TransportLeasePolicy{PortStart: 2000, PortEnd: 2000, TTL: time.Minute}
		lease, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-a", Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		store.failSave = true
		if _, err := manager.Release(lease.AgentID, lease.LeaseID, lease.Generation); err == nil {
			t.Fatal("release succeeded despite save failure")
		}
		store.failSave = false
		if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1}); !errors.Is(err, ErrTransportLeaseCapacity) {
			t.Fatalf("failed release leaked into memory: %v", err)
		}
		if released, err := manager.Release(lease.AgentID, lease.LeaseID, lease.Generation); err != nil || !released {
			t.Fatalf("exact release = %v, %v", released, err)
		}
		if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker-b", Generation: 1}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTransportLeaseLoadPrunesExpiredRecords(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	store := NewMemoryTransportLeaseStore()
	if err := store.Save([]TransportLease{{
		AgentID: "worker-a", LeaseID: "expired", Slot: 0, RemotePort: 2000,
		Generation: 1, Current: true, RenewedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTransportLeaseManager(store, fixedClock(now), leaseIDSequence("unused")); err != nil {
		t.Fatal(err)
	}
	leases, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Fatalf("persisted expired leases = %#v", leases)
	}
}

func TestFileTransportLeaseStoreUsesVersionedPrivateJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	store := NewFileTransportLeaseStore(path)
	lease := TransportLease{
		AgentID: "worker-a", LeaseID: "lease-a", Slot: 0, RemotePort: 2000,
		Generation: 1, Current: true, RenewedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := store.Save([]TransportLease{lease}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %o, want 600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`"version": 1`)) || bytes.Contains(content, []byte("auth_token")) {
		t.Fatalf("unexpected store document: %s", content)
	}
	missing, err := NewFileTransportLeaseStore(filepath.Join(t.TempDir(), "missing.json")).Load()
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing load = %#v, %v", missing, err)
	}
}

func TestTransportLeasePolicyValidation(t *testing.T) {
	manager := newTestTransportLeaseManager(t, NewMemoryTransportLeaseStore(), fixedClock(time.Now()), leaseIDSequence("unused"))
	for _, policy := range []TransportLeasePolicy{
		{PortStart: 0, PortEnd: 1, TTL: time.Minute},
		{PortStart: 2001, PortEnd: 2000, TTL: time.Minute},
		{PortStart: 2000, PortEnd: 65536, TTL: time.Minute},
		{PortStart: 2000, PortEnd: 2001, TTL: 0},
	} {
		if _, err := manager.Acquire(policy, TransportLeaseAcquireRequest{AgentID: "worker", Generation: 1}); err == nil {
			t.Fatalf("policy %#v succeeded", policy)
		}
	}
	valid := TransportLeasePolicy{PortStart: 2000, PortEnd: 2001, TTL: time.Minute}
	for _, excluded := range [][]int{{-1}, {2}} {
		if _, err := manager.Acquire(valid, TransportLeaseAcquireRequest{AgentID: "worker", Generation: 1, ExcludedSlots: excluded}); err == nil {
			t.Fatalf("excluded slots %v succeeded", excluded)
		}
	}
}

func TestTransportLeaseManagerRejectsDuplicatePersistedPorts(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	store := NewMemoryTransportLeaseStore()
	err := store.Save([]TransportLease{
		{AgentID: "worker-a", LeaseID: "a", Slot: 0, RemotePort: 2000, Generation: 1, Current: true, RenewedAt: now, ExpiresAt: now.Add(time.Minute)},
		{AgentID: "worker-b", LeaseID: "b", Slot: 0, RemotePort: 2000, Generation: 1, Current: true, RenewedAt: now, ExpiresAt: now.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTransportLeaseManager(store, fixedClock(now), leaseIDSequence("unused")); err == nil {
		t.Fatal("duplicate persisted remote ports were accepted")
	}
}

func newTestTransportLeaseManager(t *testing.T, store TransportLeaseStore, clock func() time.Time, ids TransportLeaseIDGenerator) *TransportLeaseManager {
	t.Helper()
	manager, err := NewTransportLeaseManager(store, clock, ids)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func fixedClock(now time.Time) func() time.Time { return func() time.Time { return now } }

func writeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

type failingTransportLeaseStore struct {
	leases   []TransportLease
	failSave bool
}

type countingTransportLeaseStore struct {
	TransportLeaseStore
	saves int
}

func (s *countingTransportLeaseStore) Save(leases []TransportLease) error {
	s.saves++
	return s.TransportLeaseStore.Save(leases)
}

func (s *failingTransportLeaseStore) Load() ([]TransportLease, error) {
	return cloneTransportLeases(s.leases), nil
}

func (s *failingTransportLeaseStore) Save(leases []TransportLease) error {
	if s.failSave {
		return errors.New("injected save failure")
	}
	s.leases = cloneTransportLeases(leases)
	return nil
}

func leaseIDSequence(ids ...string) TransportLeaseIDGenerator {
	index := 0
	return func() (string, error) {
		id := ids[index]
		index++
		return id, nil
	}
}
