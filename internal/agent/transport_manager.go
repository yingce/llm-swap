package agent

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"llm-swap/internal/protocol"
	transportcrypto "llm-swap/internal/transport"
)

type TransportGateway interface {
	GetConfigForAgentContext(context.Context, string, []string) (protocol.AgentConfigResponse, error)
	RequestTransportLeaseContext(context.Context, protocol.TransportLeaseRequest) (protocol.TransportLeaseResponse, error)
	ReleaseTransportLeaseContext(context.Context, protocol.TransportLeaseRequest) error
}

type RuntimeTransportSnapshot struct {
	Ready          bool
	LlamaSwapURL   string
	LlamaSwapToken string
	LeaseID        string
	Slot           int
	Generation     uint64
}

type RuntimeTransportState struct {
	mu       sync.RWMutex
	snapshot RuntimeTransportSnapshot
}

func (s *RuntimeTransportState) Snapshot() RuntimeTransportSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *RuntimeTransportState) publish(snapshot RuntimeTransportSnapshot) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
}

func (s *RuntimeTransportState) clear() {
	s.publish(RuntimeTransportSnapshot{})
}

type TransportManager struct {
	AgentID    string
	Tags       []string
	AgentToken string
	SwapPort   int
	Gateway    TransportGateway
	Factory    FRPClientFactory

	PollInterval    time.Duration
	RetryMin        time.Duration
	RetryMax        time.Duration
	ShutdownTimeout time.Duration
	ReleaseTimeout  time.Duration
	Wait            func(context.Context, time.Duration) error
	Now             func() time.Time
	Logf            func(string, ...any)

	state RuntimeTransportState
}

func (m *TransportManager) Snapshot() RuntimeTransportSnapshot {
	return m.state.Snapshot()
}

func (m *TransportManager) Run(ctx context.Context) error {
	if m.Gateway == nil || m.Factory == nil {
		return errors.New("transport dependencies unavailable")
	}
	m.state.clear()
	retryDelay := m.retryMin()
	var active *activeTransport
	var replacement *transportReplacement

	for {
		if active == nil {
			desired, err := m.fetchDesired(ctx)
			if err != nil {
				m.log("transport configuration unavailable")
				if !m.waitRetryWithReplacement(ctx, retryDelay, replacement) {
					return nil
				}
				retryDelay = m.nextRetry(retryDelay)
				continue
			}
			if replacement != nil && replacement.generation != desired.generation {
				m.releaseReplacement(replacement)
				replacement = nil
				if ctx.Err() != nil {
					return nil
				}
			}

			leaseRequest := protocol.TransportLeaseRequest{
				AgentID: m.AgentID, Tags: append([]string(nil), m.Tags...), Generation: desired.generation,
			}
			if replacement != nil && replacement.generation == desired.generation {
				leaseRequest.LeaseID = replacement.leaseID
				leaseRequest.ExcludeSlots = []int{replacement.slot}
			}
			lease, err := m.Gateway.RequestTransportLeaseContext(ctx, leaseRequest)
			if err != nil {
				m.log("transport lease unavailable")
				if !m.waitRetryWithReplacement(ctx, retryDelay, replacement) {
					return nil
				}
				retryDelay = m.nextRetry(retryDelay)
				continue
			}
			if err := validateTransportLease(desired, lease, m.now()); err != nil {
				m.log("transport lease unavailable")
				if !m.waitRetryWithReplacement(ctx, retryDelay, replacement) {
					return nil
				}
				retryDelay = m.nextRetry(retryDelay)
				continue
			}

			started, startResult := m.start(ctx, desired, lease)
			switch startResult {
			case transportStartReady:
				active = started
				replacement = nil
				retryDelay = m.retryMin()
			case transportStartShutdown:
				if m.stopClient(started) {
					m.releaseLease(lease)
				}
				return nil
			case transportStartBootstrapUnavailable:
				if !m.stopClient(started) {
					return m.parkUnconverged(ctx)
				}
				m.releaseLease(lease)
				replacement = nil
				m.log("transport client unavailable")
				if !m.waitRetry(ctx, retryDelay) {
					return nil
				}
				retryDelay = m.nextRetry(retryDelay)
			case transportStartRegistrationFailed:
				if !m.stopClient(started) {
					return m.parkUnconverged(ctx)
				}
				replacement = &transportReplacement{leaseID: lease.LeaseID, slot: lease.Slot, generation: lease.Generation}
				m.log("transport client unavailable")
				if !m.waitRetryWithReplacement(ctx, retryDelay, replacement) {
					return nil
				}
				retryDelay = m.nextRetry(retryDelay)
			}
			continue
		}

		cycleCtx, cancelCycle := context.WithCancel(ctx)
		pollDone := make(chan error, 1)
		go func() { pollDone <- m.wait(cycleCtx, m.pollInterval()) }()
		select {
		case <-ctx.Done():
			cancelCycle()
			if m.stopClient(active) {
				m.releaseLease(active.lease)
			}
			return nil
		case <-active.runDone:
			cancelCycle()
			lease := active.lease
			if !m.stopClient(active) {
				return m.parkUnconverged(ctx)
			}
			replacement = &transportReplacement{leaseID: lease.LeaseID, slot: lease.Slot, generation: lease.Generation}
			active = nil
			m.log("transport client stopped")
			if !m.waitRetryWithReplacement(ctx, retryDelay, replacement) {
				return nil
			}
			retryDelay = m.nextRetry(retryDelay)
		case err := <-pollDone:
			cancelCycle()
			if err != nil {
				if ctx.Err() != nil {
					if m.stopClient(active) {
						m.releaseLease(active.lease)
					}
					return nil
				}
				continue
			}
			desired, err := m.fetchDesired(ctx)
			if err != nil {
				m.log("transport configuration unavailable")
				continue
			}
			if active.desired == desired {
				continue
			}
			if !m.stopClient(active) {
				return m.parkUnconverged(ctx)
			}
			m.releaseLease(active.lease)
			active = nil
			replacement = nil
			retryDelay = m.retryMin()
		}
	}
}

type desiredTransport struct {
	generation uint64
	bootstrap  transportcrypto.Bootstrap
}

type activeTransport struct {
	desired     desiredTransport
	lease       protocol.TransportLeaseResponse
	client      FRPClient
	cancel      context.CancelFunc
	runDone     <-chan error
	workersDone <-chan struct{}
}

type transportReplacement struct {
	leaseID    string
	slot       int
	generation uint64
}

type transportStartResult int

const (
	transportStartBootstrapUnavailable transportStartResult = iota
	transportStartRegistrationFailed
	transportStartReady
	transportStartShutdown
)

func (m *TransportManager) fetchDesired(ctx context.Context) (desiredTransport, error) {
	response, err := m.Gateway.GetConfigForAgentContext(ctx, m.AgentID, append([]string(nil), m.Tags...))
	if err != nil || response.Transport == nil {
		return desiredTransport{}, errors.New("transport configuration unavailable")
	}
	bootstrap, err := transportcrypto.OpenBootstrap(m.AgentToken, m.AgentID, *response.Transport)
	if err != nil || response.Transport.Generation == 0 || m.SwapPort <= 0 || m.SwapPort > 65535 {
		return desiredTransport{}, errors.New("transport configuration unavailable")
	}
	bootstrap, err = normalizeTransportBootstrap(bootstrap)
	if err != nil {
		return desiredTransport{}, errors.New("transport configuration unavailable")
	}
	return desiredTransport{generation: response.Transport.Generation, bootstrap: bootstrap}, nil
}

func normalizeTransportBootstrap(bootstrap transportcrypto.Bootstrap) (transportcrypto.Bootstrap, error) {
	serverAddr := strings.TrimSpace(bootstrap.ServerAddr)
	if bootstrap.Type != "frp_tcp" || !validTransportServerAddr(serverAddr) || bootstrap.ServerPort <= 0 || bootstrap.ServerPort > 65535 ||
		strings.TrimSpace(bootstrap.AuthToken) == "" || bootstrap.PortStart <= 0 || bootstrap.PortStart > 65535 ||
		bootstrap.PortEnd < bootstrap.PortStart || bootstrap.PortEnd > 65535 || bootstrap.LeaseTTLSeconds <= 0 ||
		strings.TrimSpace(bootstrap.LlamaSwapToken) == "" {
		return transportcrypto.Bootstrap{}, errors.New("invalid transport bootstrap")
	}
	bootstrap.ServerAddr = serverAddr
	return bootstrap, nil
}

func validTransportServerAddr(serverAddr string) bool {
	if serverAddr == "" || strings.ContainsAny(serverAddr, " \t\r\n/\\[]") {
		return false
	}
	if net.ParseIP(serverAddr) != nil {
		return true
	}
	if strings.Contains(serverAddr, ":") || len(serverAddr) > 253 {
		return false
	}
	for _, label := range strings.Split(serverAddr, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validateTransportLease(desired desiredTransport, lease protocol.TransportLeaseResponse, now time.Time) error {
	if lease.Generation != desired.generation || strings.TrimSpace(lease.LeaseID) == "" || strings.TrimSpace(lease.LeaseID) != lease.LeaseID ||
		lease.Slot < 0 || lease.Slot > desired.bootstrap.PortEnd-desired.bootstrap.PortStart ||
		lease.RemotePort < desired.bootstrap.PortStart || lease.RemotePort > desired.bootstrap.PortEnd ||
		lease.ExpiresAt.IsZero() || !lease.ExpiresAt.After(now) {
		return errors.New("invalid transport lease")
	}
	if lease.RemotePort != desired.bootstrap.PortStart+lease.Slot {
		return errors.New("invalid transport lease")
	}
	return nil
}

func (m *TransportManager) start(ctx context.Context, desired desiredTransport, lease protocol.TransportLeaseResponse) (*activeTransport, transportStartResult) {
	client, err := m.Factory.New(FRPProxyConfig{
		ServerAddr: desired.bootstrap.ServerAddr, ServerPort: desired.bootstrap.ServerPort,
		AuthToken: desired.bootstrap.AuthToken, ProxyName: frpProxyName(m.AgentID, desired.generation),
		LocalAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(m.SwapPort)), RemotePort: lease.RemotePort,
	})
	if err != nil || client == nil {
		return nil, transportStartBootstrapUnavailable
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	readyDone := make(chan error, 1)
	workersDone := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		runDone <- client.Run(runCtx)
	}()
	go func() {
		defer workers.Done()
		readyDone <- client.WaitReady(runCtx)
	}()
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	active := &activeTransport{
		desired: desired, lease: lease, client: client, cancel: cancelRun,
		runDone: runDone, workersDone: workersDone,
	}

	select {
	case <-ctx.Done():
		return active, transportStartShutdown
	case <-runDone:
		return active, transportStartBootstrapUnavailable
	case err := <-readyDone:
		if err != nil {
			if errors.Is(err, errFRPClientStoppedBeforeReady) {
				return active, transportStartBootstrapUnavailable
			}
			return active, transportStartRegistrationFailed
		}
	}
	select {
	case <-runDone:
		return active, transportStartRegistrationFailed
	default:
	}
	m.state.publish(RuntimeTransportSnapshot{
		Ready: true, LlamaSwapURL: transportURL(desired.bootstrap.ServerAddr, lease.RemotePort),
		LlamaSwapToken: desired.bootstrap.LlamaSwapToken, LeaseID: lease.LeaseID,
		Slot: lease.Slot, Generation: lease.Generation,
	})
	return active, transportStartReady
}

func (m *TransportManager) stopClient(active *activeTransport) bool {
	m.state.clear()
	if active == nil {
		return true
	}
	active.cancel()
	closeDone := make(chan struct{})
	go func() {
		_ = active.client.Close()
		close(closeDone)
	}()

	timeout := m.ShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), timeout)
	defer shutdownCancel()
	workersDone := active.workersDone
	workersConverged := false
	for workersDone != nil || closeDone != nil {
		select {
		case <-workersDone:
			workersDone = nil
			workersConverged = true
		case <-closeDone:
			closeDone = nil
		case <-shutdownCtx.Done():
			m.log("transport client shutdown timed out")
			return workersConverged
		}
	}
	return true
}

func (m *TransportManager) parkUnconverged(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (m *TransportManager) releaseLease(lease protocol.TransportLeaseResponse) {
	m.releaseLeaseExact(lease.LeaseID, lease.Generation)
}

func (m *TransportManager) releaseReplacement(replacement *transportReplacement) {
	if replacement != nil {
		m.releaseLeaseExact(replacement.leaseID, replacement.generation)
	}
}

func (m *TransportManager) releaseLeaseExact(leaseID string, generation uint64) {
	timeout := m.ReleaseTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), timeout)
	defer releaseCancel()
	if err := m.Gateway.ReleaseTransportLeaseContext(releaseCtx, protocol.TransportLeaseRequest{
		AgentID: m.AgentID, Tags: append([]string(nil), m.Tags...), Generation: generation, LeaseID: leaseID,
	}); err != nil {
		m.log("transport lease release failed")
	}
}

func (m *TransportManager) wait(ctx context.Context, delay time.Duration) error {
	if m.Wait != nil {
		return m.Wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *TransportManager) waitRetry(ctx context.Context, delay time.Duration) bool {
	return m.wait(ctx, delay) == nil
}

func (m *TransportManager) waitRetryWithReplacement(ctx context.Context, delay time.Duration, replacement *transportReplacement) bool {
	if m.waitRetry(ctx, delay) {
		return true
	}
	if ctx.Err() != nil {
		m.releaseReplacement(replacement)
	}
	return false
}

func (m *TransportManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *TransportManager) pollInterval() time.Duration {
	if m.PollInterval > 0 {
		return m.PollInterval
	}
	return 30 * time.Second
}

func (m *TransportManager) retryMin() time.Duration {
	if m.RetryMin > 0 {
		return m.RetryMin
	}
	return time.Second
}

func (m *TransportManager) nextRetry(current time.Duration) time.Duration {
	maximum := m.RetryMax
	if maximum <= 0 {
		maximum = 30 * time.Second
	}
	if current >= maximum {
		return maximum
	}
	next := current * 2
	if next > maximum {
		return maximum
	}
	return next
}

func (m *TransportManager) log(message string) {
	if m.Logf != nil {
		m.Logf("%s", message)
	}
}

func transportURL(serverAddr string, remotePort int) string {
	return "http://" + net.JoinHostPort(serverAddr, strconv.Itoa(remotePort))
}
