package agent

import (
	"context"
	"errors"
	"net"
	"strconv"
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

	PollInterval   time.Duration
	RetryMin       time.Duration
	RetryMax       time.Duration
	ReleaseTimeout time.Duration
	Wait           func(context.Context, time.Duration) error
	Logf           func(string, ...any)

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
				if !m.waitRetry(ctx, retryDelay) {
					return nil
				}
				retryDelay = m.nextRetry(retryDelay)
				continue
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
				if !m.waitRetry(ctx, retryDelay) {
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
				m.stopClient(started)
				m.releaseLease(lease)
				return nil
			default:
				m.stopClient(started)
				replacement = &transportReplacement{leaseID: lease.LeaseID, slot: lease.Slot, generation: lease.Generation}
				m.log("transport client unavailable")
				if !m.waitRetry(ctx, retryDelay) {
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
			m.stopClient(active)
			m.releaseLease(active.lease)
			return nil
		case <-active.runDone:
			cancelCycle()
			lease := active.lease
			m.stopClient(active)
			replacement = &transportReplacement{leaseID: lease.LeaseID, slot: lease.Slot, generation: lease.Generation}
			active = nil
			m.log("transport client stopped")
			if !m.waitRetry(ctx, retryDelay) {
				return nil
			}
			retryDelay = m.nextRetry(retryDelay)
		case err := <-pollDone:
			cancelCycle()
			if err != nil {
				if ctx.Err() != nil {
					m.stopClient(active)
					m.releaseLease(active.lease)
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
			m.stopClient(active)
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
	desired desiredTransport
	lease   protocol.TransportLeaseResponse
	client  FRPClient
	cancel  context.CancelFunc
	runDone <-chan error
}

type transportReplacement struct {
	leaseID    string
	slot       int
	generation uint64
}

type transportStartResult int

const (
	transportStartFailed transportStartResult = iota
	transportStartReady
	transportStartShutdown
)

func (m *TransportManager) fetchDesired(ctx context.Context) (desiredTransport, error) {
	response, err := m.Gateway.GetConfigForAgentContext(ctx, m.AgentID, append([]string(nil), m.Tags...))
	if err != nil || response.Transport == nil {
		return desiredTransport{}, errors.New("transport configuration unavailable")
	}
	bootstrap, err := transportcrypto.OpenBootstrap(m.AgentToken, m.AgentID, *response.Transport)
	if err != nil || bootstrap.Type != "frp_tcp" || bootstrap.ServerAddr == "" || bootstrap.ServerPort <= 0 {
		return desiredTransport{}, errors.New("transport configuration unavailable")
	}
	return desiredTransport{generation: response.Transport.Generation, bootstrap: bootstrap}, nil
}

func (m *TransportManager) start(ctx context.Context, desired desiredTransport, lease protocol.TransportLeaseResponse) (*activeTransport, transportStartResult) {
	client, err := m.Factory.New(FRPProxyConfig{
		ServerAddr: desired.bootstrap.ServerAddr, ServerPort: desired.bootstrap.ServerPort,
		AuthToken: desired.bootstrap.AuthToken, ProxyName: frpProxyName(m.AgentID, desired.generation),
		LocalAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(m.SwapPort)), RemotePort: lease.RemotePort,
	})
	if err != nil || client == nil {
		return nil, transportStartFailed
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(runCtx) }()
	readyDone := make(chan error, 1)
	go func() { readyDone <- client.WaitReady(runCtx) }()
	active := &activeTransport{desired: desired, lease: lease, client: client, cancel: cancelRun, runDone: runDone}

	select {
	case <-ctx.Done():
		return active, transportStartShutdown
	case <-runDone:
		return active, transportStartFailed
	case err := <-readyDone:
		if err != nil {
			return active, transportStartFailed
		}
	}
	select {
	case <-runDone:
		return active, transportStartFailed
	default:
	}
	m.state.publish(RuntimeTransportSnapshot{
		Ready: true, LlamaSwapURL: transportURL(desired.bootstrap.ServerAddr, lease.RemotePort),
		LlamaSwapToken: desired.bootstrap.LlamaSwapToken, LeaseID: lease.LeaseID,
		Slot: lease.Slot, Generation: lease.Generation,
	})
	return active, transportStartReady
}

func (m *TransportManager) stopClient(active *activeTransport) {
	if active == nil {
		m.state.clear()
		return
	}
	active.cancel()
	_ = active.client.Close()
	m.state.clear()
}

func (m *TransportManager) releaseLease(lease protocol.TransportLeaseResponse) {
	timeout := m.ReleaseTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), timeout)
	defer releaseCancel()
	if err := m.Gateway.ReleaseTransportLeaseContext(releaseCtx, protocol.TransportLeaseRequest{
		AgentID: m.AgentID, Tags: append([]string(nil), m.Tags...), Generation: lease.Generation, LeaseID: lease.LeaseID,
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
