package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrTransportLeaseCapacity    = errors.New("transport lease capacity exhausted")
	ErrTransportLeaseConflict    = errors.New("transport lease conflict")
	ErrTransportLeaseIDCollision = errors.New("transport lease id collision")
	ErrTransportLeaseNotFound    = errors.New("transport lease not found")
)

const transportLeaseIDGenerationAttempts = 8

type TransportLeasePolicy struct {
	PortStart int
	PortEnd   int
	TTL       time.Duration
}

type TransportLeaseAcquireRequest struct {
	AgentID        string
	Generation     uint64
	CurrentLeaseID string
	ExcludedSlots  []int
}

type TransportLease struct {
	AgentID    string    `json:"agent_id"`
	LeaseID    string    `json:"lease_id"`
	Slot       int       `json:"slot"`
	RemotePort int       `json:"remote_port"`
	Generation uint64    `json:"generation"`
	Current    bool      `json:"current"`
	RenewedAt  time.Time `json:"renewed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type TransportLeaseIDGenerator func() (string, error)

type TransportLeaseManager struct {
	mu     sync.Mutex
	store  TransportLeaseStore
	now    func() time.Time
	newID  TransportLeaseIDGenerator
	leases []TransportLease
}

func NewTransportLeaseManager(store TransportLeaseStore, now func() time.Time, newID TransportLeaseIDGenerator) (*TransportLeaseManager, error) {
	if store == nil {
		store = NewMemoryTransportLeaseStore()
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = randomTransportLeaseID
	}
	leases, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load transport leases: %w", err)
	}
	current := now()
	pruned := pruneExpiredTransportLeases(leases, current)
	if err := validateLoadedTransportLeases(pruned); err != nil {
		return nil, fmt.Errorf("validate transport leases: %w", err)
	}
	if len(pruned) != len(leases) {
		if err := store.Save(pruned); err != nil {
			return nil, fmt.Errorf("prune expired transport leases: %w", err)
		}
	}
	manager := &TransportLeaseManager{store: store, now: now, newID: newID, leases: pruned}
	return manager, nil
}

// LookupCurrent returns an exact, live lease without renewing or otherwise
// mutating the manager or its persistent store.
func (m *TransportLeaseManager) LookupCurrent(agentID, leaseID string, generation uint64) (TransportLease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, lease := range m.leases {
		if lease.Current && lease.AgentID == agentID && lease.LeaseID == leaseID && lease.Generation == generation && lease.ExpiresAt.After(now) {
			return lease, nil
		}
	}
	return TransportLease{}, ErrTransportLeaseNotFound
}

func (m *TransportLeaseManager) Renew(policy TransportLeasePolicy, agentID, leaseID string, generation uint64) (TransportLease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateTransportLeasePolicy(policy); err != nil {
		return TransportLease{}, err
	}
	now := m.now()
	match := -1
	for i := range m.leases {
		lease := m.leases[i]
		if lease.Current && lease.AgentID == agentID && lease.LeaseID == leaseID && lease.Generation == generation && lease.ExpiresAt.After(now) && transportLeaseMatchesPolicy(lease, policy) {
			match = i
			if lease.ExpiresAt.Sub(now) > policy.TTL/2 {
				return lease, nil
			}
			break
		}
	}
	if match < 0 {
		return TransportLease{}, ErrTransportLeaseNotFound
	}
	candidate := cloneTransportLeases(m.leases)
	candidate[match].RenewedAt = now
	candidate[match].ExpiresAt = now.Add(policy.TTL)
	if err := m.store.Save(candidate); err != nil {
		return TransportLease{}, fmt.Errorf("save transport leases: %w", err)
	}
	m.leases = candidate
	return candidate[match], nil
}

func (m *TransportLeaseManager) Release(agentID, leaseID string, generation uint64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	candidate := pruneExpiredTransportLeases(m.leases, now)
	match := -1
	for i := range candidate {
		lease := candidate[i]
		if lease.Current && lease.AgentID == agentID && lease.LeaseID == leaseID && lease.Generation == generation {
			match = i
			break
		}
	}
	if match >= 0 {
		candidate = append(candidate[:match], candidate[match+1:]...)
	}
	if match >= 0 || len(candidate) != len(m.leases) {
		if err := m.store.Save(candidate); err != nil {
			return false, fmt.Errorf("save transport leases: %w", err)
		}
		m.leases = candidate
	}
	return match >= 0, nil
}

func (m *TransportLeaseManager) Acquire(policy TransportLeasePolicy, request TransportLeaseAcquireRequest) (TransportLease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateTransportLeasePolicy(policy); err != nil {
		return TransportLease{}, err
	}
	if strings.TrimSpace(request.AgentID) == "" || request.Generation == 0 {
		return TransportLease{}, fmt.Errorf("invalid transport lease request")
	}
	for _, slot := range request.ExcludedSlots {
		if slot < 0 || slot > policy.PortEnd-policy.PortStart {
			return TransportLease{}, fmt.Errorf("invalid excluded transport lease slot %d", slot)
		}
	}
	now := m.now()
	expiredOwnedLeaseID := false
	if request.CurrentLeaseID != "" {
		for _, lease := range m.leases {
			if lease.AgentID == request.AgentID && lease.LeaseID == request.CurrentLeaseID && !lease.ExpiresAt.After(now) {
				expiredOwnedLeaseID = true
				break
			}
		}
	}
	candidate := pruneExpiredTransportLeases(m.leases, now)
	currentIndex := -1
	for i := range candidate {
		if candidate[i].Current && candidate[i].AgentID == request.AgentID {
			currentIndex = i
			break
		}
	}
	if currentIndex < 0 {
		if request.CurrentLeaseID != "" && !expiredOwnedLeaseID {
			return TransportLease{}, ErrTransportLeaseConflict
		}
	} else {
		current := candidate[currentIndex]
		if request.CurrentLeaseID != "" && request.CurrentLeaseID != current.LeaseID {
			return TransportLease{}, ErrTransportLeaseConflict
		}
		sticky := current.Generation == request.Generation &&
			transportLeaseMatchesPolicy(current, policy) &&
			!containsSlot(request.ExcludedSlots, current.Slot)
		if !sticky && request.CurrentLeaseID != current.LeaseID {
			return TransportLease{}, ErrTransportLeaseConflict
		}
	}
	for i := range candidate {
		lease := &candidate[i]
		if lease.Current && lease.AgentID == request.AgentID && lease.Generation == request.Generation && transportLeaseMatchesPolicy(*lease, policy) && !containsSlot(request.ExcludedSlots, lease.Slot) {
			lease.RenewedAt = now
			lease.ExpiresAt = now.Add(policy.TTL)
			if err := m.store.Save(candidate); err != nil {
				return TransportLease{}, fmt.Errorf("save transport leases: %w", err)
			}
			m.leases = candidate
			return *lease, nil
		}
	}

	usedPorts := make(map[int]struct{}, len(candidate))
	for _, lease := range candidate {
		usedPorts[lease.RemotePort] = struct{}{}
	}
	remotePort := 0
	for port := policy.PortStart; port <= policy.PortEnd; port++ {
		slot := port - policy.PortStart
		if containsSlot(request.ExcludedSlots, slot) {
			continue
		}
		if _, used := usedPorts[port]; !used {
			remotePort = port
			break
		}
	}
	if remotePort == 0 {
		return TransportLease{}, ErrTransportLeaseCapacity
	}
	leaseID, err := m.generateUniqueLeaseID(candidate)
	if err != nil {
		return TransportLease{}, err
	}
	for i := range candidate {
		if candidate[i].Current && candidate[i].AgentID == request.AgentID {
			candidate[i].Current = false
		}
	}
	lease := TransportLease{
		AgentID: request.AgentID, LeaseID: leaseID,
		Slot: remotePort - policy.PortStart, RemotePort: remotePort,
		Generation: request.Generation, Current: true,
		RenewedAt: now, ExpiresAt: now.Add(policy.TTL),
	}
	candidate = append(candidate, lease)
	if err := m.store.Save(candidate); err != nil {
		return TransportLease{}, fmt.Errorf("save transport leases: %w", err)
	}
	m.leases = candidate
	return lease, nil
}

func (m *TransportLeaseManager) generateUniqueLeaseID(leases []TransportLease) (string, error) {
	used := make(map[string]struct{}, len(leases))
	for _, lease := range leases {
		used[lease.LeaseID] = struct{}{}
	}
	for attempt := 0; attempt < transportLeaseIDGenerationAttempts; attempt++ {
		leaseID, err := m.newID()
		if err != nil {
			return "", fmt.Errorf("generate transport lease id: %w", err)
		}
		if strings.TrimSpace(leaseID) == "" {
			continue
		}
		if _, exists := used[leaseID]; !exists {
			return leaseID, nil
		}
	}
	return "", ErrTransportLeaseIDCollision
}

func validateTransportLeasePolicy(policy TransportLeasePolicy) error {
	if policy.PortStart < 1 || policy.PortStart > 65535 {
		return fmt.Errorf("invalid transport lease port start")
	}
	if policy.PortEnd < policy.PortStart || policy.PortEnd > 65535 {
		return fmt.Errorf("invalid transport lease port end")
	}
	if policy.TTL <= 0 {
		return fmt.Errorf("invalid transport lease ttl")
	}
	return nil
}

func validateLoadedTransportLeases(leases []TransportLease) error {
	ports := make(map[int]string, len(leases))
	leaseIDs := make(map[string]struct{}, len(leases))
	currentAgents := make(map[string]string)
	for _, lease := range leases {
		if strings.TrimSpace(lease.AgentID) == "" || strings.TrimSpace(lease.LeaseID) == "" {
			return fmt.Errorf("persisted lease has empty identity")
		}
		if lease.Generation == 0 || lease.Slot < 0 || lease.RemotePort < 1 || lease.RemotePort > 65535 {
			return fmt.Errorf("persisted lease %q has invalid allocation", lease.LeaseID)
		}
		if lease.RenewedAt.IsZero() || !lease.ExpiresAt.After(lease.RenewedAt) {
			return fmt.Errorf("persisted lease %q has invalid lifetime", lease.LeaseID)
		}
		if other, exists := ports[lease.RemotePort]; exists {
			return fmt.Errorf("persisted leases %q and %q reserve remote port %d", other, lease.LeaseID, lease.RemotePort)
		}
		ports[lease.RemotePort] = lease.LeaseID
		if _, exists := leaseIDs[lease.LeaseID]; exists {
			return fmt.Errorf("persisted lease id %q is duplicated", lease.LeaseID)
		}
		leaseIDs[lease.LeaseID] = struct{}{}
		if lease.Current {
			if other, exists := currentAgents[lease.AgentID]; exists {
				return fmt.Errorf("agent %q has multiple current leases %q and %q", lease.AgentID, other, lease.LeaseID)
			}
			currentAgents[lease.AgentID] = lease.LeaseID
		}
	}
	return nil
}

func transportLeaseMatchesPolicy(lease TransportLease, policy TransportLeasePolicy) bool {
	return lease.RemotePort >= policy.PortStart && lease.RemotePort <= policy.PortEnd &&
		lease.Slot == lease.RemotePort-policy.PortStart &&
		lease.ExpiresAt.Sub(lease.RenewedAt) == policy.TTL
}

func pruneExpiredTransportLeases(leases []TransportLease, now time.Time) []TransportLease {
	result := make([]TransportLease, 0, len(leases))
	for _, lease := range leases {
		if lease.ExpiresAt.After(now) {
			result = append(result, lease)
		}
	}
	return result
}

func containsSlot(slots []int, target int) bool {
	for _, slot := range slots {
		if slot == target {
			return true
		}
	}
	return false
}

func randomTransportLeaseID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
