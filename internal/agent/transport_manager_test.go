package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"llm-swap/internal/protocol"
	transportcrypto "llm-swap/internal/transport"
)

func TestTransportManagerDecryptsAcquiresStartsAndPublishesOnlyAfterReady(t *testing.T) {
	const (
		agentID    = "worker-gpu0"
		agentToken = "agent-secret"
	)
	bootstrap := transportcrypto.Bootstrap{
		Type:            "frp_tcp",
		ServerAddr:      "198.51.100.8",
		ServerPort:      7000,
		AuthToken:       "frp-secret",
		PortStart:       2000,
		PortEnd:         3000,
		LeaseTTLSeconds: 180,
		LlamaSwapToken:  "llama-secret",
	}
	envelope, err := transportcrypto.SealBootstrap(agentToken, agentID, 7, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &transportGatewayFake{
		config:       protocol.AgentConfigResponse{Transport: &envelope},
		leases:       []protocol.TransportLeaseResponse{{LeaseID: "lease-7", Slot: 3, RemotePort: 2003, Generation: 7, ExpiresAt: time.Now().Add(time.Minute)}},
		acquireCalls: make(chan protocol.TransportLeaseRequest, 32),
		releaseCalls: make(chan protocol.TransportLeaseRequest, 32),
	}
	client := newFRPClientFake()
	factory := &transportFactoryFake{clients: []*frpClientFake{client}, created: make(chan FRPProxyConfig, 32)}
	manager := &TransportManager{
		AgentID: agentID, Tags: []string{"gpu-4090"}, AgentToken: agentToken,
		SwapPort: 6006, Gateway: gateway, Factory: factory,
		PollInterval: time.Hour, RetryMin: time.Millisecond, RetryMax: time.Millisecond,
		ReleaseTimeout: time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	config := receiveWithTimeout(t, factory.created)
	if config.ServerAddr != bootstrap.ServerAddr || config.ServerPort != bootstrap.ServerPort || config.AuthToken != bootstrap.AuthToken {
		t.Fatalf("FRP server config = %+v", config)
	}
	if config.LocalAddr != net.JoinHostPort("127.0.0.1", strconv.Itoa(manager.SwapPort)) || config.RemotePort != 2003 {
		t.Fatalf("FRP proxy endpoints = %+v", config)
	}
	if config.ProxyName != frpProxyName(agentID, 7) {
		t.Fatalf("proxy name = %q", config.ProxyName)
	}
	request := receiveWithTimeout(t, gateway.acquireCalls)
	if request.AgentID != agentID || request.Generation != 7 || len(request.Tags) != 1 || request.Tags[0] != "gpu-4090" {
		t.Fatalf("lease request = %+v", request)
	}
	if snapshot := manager.Snapshot(); snapshot.Ready || snapshot.LlamaSwapURL != "" {
		t.Fatalf("state published before readiness: %+v", snapshot)
	}

	client.ready <- nil
	eventually(t, func() bool { return manager.Snapshot().Ready })
	snapshot := manager.Snapshot()
	if snapshot.LlamaSwapURL != "http://198.51.100.8:2003" || snapshot.LlamaSwapToken != "llama-secret" || snapshot.LeaseID != "lease-7" || snapshot.Slot != 3 || snapshot.Generation != 7 {
		t.Fatalf("published state = %+v", snapshot)
	}

	cancel()
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestTransportManagerDoesNotRestartForUnchangedDecodedConfig(t *testing.T) {
	manager, gateway, factory, first, waiter, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	defer stopTransportManager(t, cancel, done)

	// Resealing the same payload produces a fresh nonce. Reconciliation must
	// compare the decoded bootstrap rather than the random envelope.
	gateway.setConfig(sealedTransportConfig(t, manager.AgentToken, manager.AgentID, 7, testTransportBootstrap("frps.example.test")))
	waiter.advance(t)
	receiveWithTimeout(t, gateway.configCalls)
	eventually(t, func() bool { return gateway.acquireCount() == 1 })
	if got := factory.createCount(); got != 1 {
		t.Fatalf("FRP clients created = %d, want 1", got)
	}
	select {
	case <-first.closed:
		t.Fatal("unchanged decoded config closed healthy client")
	default:
	}
}

func TestTransportManagerConfigChangeClosesReleasesThenStartsFreshLease(t *testing.T) {
	manager, gateway, factory, first, waiter, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	defer stopTransportManager(t, cancel, done)

	second := newFRPClientFake()
	factory.addClient(second)
	gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "lease-8", Slot: 4, RemotePort: 2004, Generation: 8})
	gateway.setConfig(sealedTransportConfig(t, manager.AgentToken, manager.AgentID, 8, testTransportBootstrap("frps-next.example.test")))
	waiter.advance(t)

	receiveWithTimeout(t, first.closed)
	release := receiveWithTimeout(t, gateway.releaseCalls)
	if release.LeaseID != "lease-7" || release.Generation != 7 || release.AgentID != manager.AgentID {
		t.Fatalf("release request = %+v", release)
	}
	created := receiveWithTimeout(t, factory.created)
	if created.ServerAddr != "frps-next.example.test" || created.RemotePort != 2004 {
		t.Fatalf("replacement config = %+v", created)
	}
	if snapshot := manager.Snapshot(); snapshot.Ready {
		t.Fatalf("replacement published before readiness: %+v", snapshot)
	}
	second.ready <- nil
	eventually(t, func() bool { return manager.Snapshot().Generation == 8 && manager.Snapshot().Ready })
}

func TestTransportManagerBootstrapChangeWithinGenerationRestartsTransport(t *testing.T) {
	manager, gateway, factory, first, waiter, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	defer stopTransportManager(t, cancel, done)

	second := newFRPClientFake()
	factory.addClient(second)
	gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "lease-new-bootstrap", Slot: 4, RemotePort: 2004, Generation: 7})
	gateway.setConfig(sealedTransportConfig(t, manager.AgentToken, manager.AgentID, 7, testTransportBootstrap("frps-next.example.test")))
	waiter.advance(t)

	receiveWithTimeout(t, first.closed)
	receiveWithTimeout(t, gateway.releaseCalls)
	created := receiveWithTimeout(t, factory.created)
	if created.ServerAddr != "frps-next.example.test" {
		t.Fatalf("replacement server = %q", created.ServerAddr)
	}
	second.ready <- nil
	eventually(t, func() bool { return manager.Snapshot().LeaseID == "lease-new-bootstrap" && manager.Snapshot().Ready })
}

func TestTransportManagerStartupFailureReplacesAndExcludesSuspectLease(t *testing.T) {
	for _, test := range []struct {
		name string
		fail func(*frpClientFake)
	}{
		{name: "run exits before readiness", fail: func(client *frpClientFake) { client.runErr <- errors.New("run failed") }},
		{name: "readiness fails", fail: func(client *frpClientFake) { client.ready <- errors.New("not ready") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			const agentID, agentToken = "worker-gpu0", "agent-secret"
			gateway := newTransportGatewayFake(sealedTransportConfig(t, agentToken, agentID, 7, testTransportBootstrap("frps.example.test")))
			gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "suspect", Slot: 2, RemotePort: 2002, Generation: 7})
			gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "replacement", Slot: 3, RemotePort: 2003, Generation: 7})
			first, second := newFRPClientFake(), newFRPClientFake()
			factory := newTransportFactoryFake(first, second)
			waiter := newManualTransportWaiter()
			manager := &TransportManager{AgentID: agentID, Tags: []string{"gpu-4090"}, AgentToken: agentToken, SwapPort: 6006,
				Gateway: gateway, Factory: factory, RetryMin: time.Second, RetryMax: time.Second, PollInterval: time.Hour,
				ReleaseTimeout: time.Second, Wait: waiter.Wait}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- manager.Run(ctx) }()
			receiveWithTimeout(t, gateway.configCalls)
			firstRequest := receiveWithTimeout(t, gateway.acquireCalls)
			if firstRequest.LeaseID != "" || len(firstRequest.ExcludeSlots) != 0 {
				t.Fatalf("initial request = %+v", firstRequest)
			}
			receiveWithTimeout(t, factory.created)
			test.fail(first)
			receiveWithTimeout(t, first.closed)
			waiter.advance(t)
			receiveWithTimeout(t, gateway.configCalls)
			replacement := receiveWithTimeout(t, gateway.acquireCalls)
			if replacement.LeaseID != "suspect" || len(replacement.ExcludeSlots) != 1 || replacement.ExcludeSlots[0] != 2 {
				t.Fatalf("replacement request = %+v", replacement)
			}
			select {
			case release := <-gateway.releaseCalls:
				t.Fatalf("suspect lease was released: %+v", release)
			default:
			}
			receiveWithTimeout(t, factory.created)
			second.ready <- nil
			eventually(t, func() bool { return manager.Snapshot().LeaseID == "replacement" && manager.Snapshot().Ready })
			stopTransportManager(t, cancel, done)
		})
	}
}

func TestTransportManagerFatalExitClearsStateBeforeReplacement(t *testing.T) {
	manager, gateway, factory, first, waiter, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	second := newFRPClientFake()
	factory.addClient(second)
	gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "replacement", Slot: 4, RemotePort: 2004, Generation: 7})

	first.runErr <- errors.New("fatal")
	receiveWithTimeout(t, first.closed)
	eventually(t, func() bool { return !manager.Snapshot().Ready && manager.Snapshot().LlamaSwapURL == "" })
	waiter.advanceDuration(t, time.Second)
	receiveWithTimeout(t, gateway.configCalls)
	request := receiveWithTimeout(t, gateway.acquireCalls)
	if request.LeaseID != "lease-7" || len(request.ExcludeSlots) != 1 || request.ExcludeSlots[0] != 3 {
		t.Fatalf("replacement request = %+v", request)
	}
	receiveWithTimeout(t, factory.created)
	second.ready <- nil
	eventually(t, func() bool { return manager.Snapshot().LeaseID == "replacement" && manager.Snapshot().Ready })
	stopTransportManager(t, cancel, done)
}

func TestTransportManagerCancellationAfterClientFailureReleasesExactLease(t *testing.T) {
	for _, test := range []struct {
		name  string
		ready bool
		fail  func(*frpClientFake)
	}{
		{name: "run exits during startup", fail: func(client *frpClientFake) { client.runErr <- errors.New("run failed") }},
		{name: "readiness fails", fail: func(client *frpClientFake) { client.ready <- errors.New("not ready") }},
		{name: "fatal exit after ready", ready: true, fail: func(client *frpClientFake) { client.runErr <- errors.New("fatal") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			const agentID, agentToken = "worker-gpu0", "agent-secret"
			gateway := newTransportGatewayFake(sealedTransportConfig(t, agentToken, agentID, 7, testTransportBootstrap("frps.example.test")))
			gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "lease-exact", Slot: 2, RemotePort: 2002, Generation: 7, ExpiresAt: time.Now().Add(time.Minute)})
			client := newFRPClientFake()
			factory := newTransportFactoryFake(client)
			manager := &TransportManager{AgentID: agentID, AgentToken: agentToken, SwapPort: 6006,
				Gateway: gateway, Factory: factory, RetryMin: time.Hour, ReleaseTimeout: time.Second}
			ctx, cancel := context.WithCancel(context.Background())
			client.closeHook = cancel
			done := make(chan error, 1)
			go func() { done <- manager.Run(ctx) }()
			receiveWithTimeout(t, factory.created)
			if test.ready {
				client.ready <- nil
				eventually(t, func() bool { return manager.Snapshot().Ready })
			}
			test.fail(client)
			if err := receiveWithTimeout(t, done); err != nil {
				t.Fatalf("Run returned %v", err)
			}
			release := receiveWithTimeout(t, gateway.releaseCalls)
			if release.AgentID != agentID || release.LeaseID != "lease-exact" || release.Generation != 7 {
				t.Fatalf("release request = %+v", release)
			}
			select {
			case duplicate := <-gateway.releaseCalls:
				t.Fatalf("duplicate release = %+v", duplicate)
			default:
			}
		})
	}
}

func TestTransportURLUsesIPv6SafeHostFormatting(t *testing.T) {
	if got := transportURL("2001:db8::8", 2003); got != "http://[2001:db8::8]:2003" {
		t.Fatalf("IPv6 URL = %q", got)
	}
}

func TestTransportManagerShutdownClosesClearsThenReleasesExactLease(t *testing.T) {
	manager, gateway, _, client, _, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	events := make(chan string, 4)
	client.events = events
	gateway.events = events

	cancel()
	if first := receiveWithTimeout(t, events); first != "close" {
		t.Fatalf("first shutdown event = %q, want close", first)
	}
	if snapshot := manager.Snapshot(); snapshot.Ready || snapshot.LeaseID != "" {
		t.Fatalf("state not cleared before release: %+v", snapshot)
	}
	if second := receiveWithTimeout(t, events); second != "release" {
		t.Fatalf("second shutdown event = %q, want release", second)
	}
	release := receiveWithTimeout(t, gateway.releaseCalls)
	if release.AgentID != manager.AgentID || release.LeaseID != "lease-7" || release.Generation != 7 || len(release.Tags) != 1 || release.Tags[0] != "gpu-4090" {
		t.Fatalf("release request = %+v", release)
	}
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestTransportManagerClearsBeforeCloseAndBoundsBlockedClose(t *testing.T) {
	manager, _, _, client, _, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	manager.ShutdownTimeout = 10 * time.Millisecond
	client.closeBlock = make(chan struct{})
	stateAtClose := make(chan RuntimeTransportSnapshot, 1)
	client.closeInspect = func() { stateAtClose <- manager.Snapshot() }
	var mu sync.Mutex
	var logs []string
	manager.Logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	cancel()
	if snapshot := receiveWithTimeout(t, stateAtClose); snapshot.Ready || snapshot.LeaseID != "" || snapshot.LlamaSwapToken != "" {
		t.Fatalf("state at Close = %+v, want cleared", snapshot)
	}
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	close(client.closeBlock)
	receiveWithTimeout(t, client.closed)
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "transport client shutdown timed out") {
		t.Fatalf("shutdown logs = %q", joined)
	}
}

func TestNormalizeTransportBootstrapAcceptsTrimmedHostAndUnbracketedIP(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "  frps.example.test  ", want: "frps.example.test"},
		{input: "198.51.100.8", want: "198.51.100.8"},
		{input: "2001:db8::8", want: "2001:db8::8"},
	} {
		bootstrap := testTransportBootstrap(test.input)
		got, err := normalizeTransportBootstrap(bootstrap)
		if err != nil {
			t.Fatalf("normalize %q: %v", test.input, err)
		}
		if got.ServerAddr != test.want {
			t.Fatalf("normalized server = %q, want %q", got.ServerAddr, test.want)
		}
	}
}

func TestTransportManagerWaitsForCooperativeRunGoroutineToConverge(t *testing.T) {
	manager, gateway, _, client, _, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	manager.ShutdownTimeout = time.Second
	client.runExitBlock = make(chan struct{})
	eventually(t, func() bool { return client.activeCalls.Load() == 1 })
	if got := client.activeCalls.Load(); got != 1 {
		t.Fatalf("active client calls before shutdown = %d, want 1", got)
	}

	cancel()
	receiveWithTimeout(t, client.closed)
	select {
	case err := <-done:
		close(client.runExitBlock)
		t.Fatalf("manager returned before cooperative Run exited: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(client.runExitBlock)
	receiveWithTimeout(t, gateway.releaseCalls)
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if got := client.activeCalls.Load(); got != 0 {
		t.Fatalf("active client calls after shutdown = %d, want 0", got)
	}
}

func TestTransportManagerReleaseFailureIsBoundedAndLoggedGenerically(t *testing.T) {
	manager, gateway, _, _, _, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	manager.ReleaseTimeout = 10 * time.Millisecond
	gateway.releaseFn = func(ctx context.Context, _ protocol.TransportLeaseRequest) error {
		<-ctx.Done()
		return errors.New("frp-secret release detail")
	}
	var mu sync.Mutex
	var logs []string
	manager.Logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	cancel()
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	if strings.Contains(joined, "frp-secret") || !strings.Contains(joined, "transport lease release failed") {
		t.Fatalf("release logs = %q", joined)
	}
}

func TestTransportManagerTamperedOrWrongTokenStaysNotReadyWithoutLeakingSecrets(t *testing.T) {
	for _, test := range []struct {
		name  string
		token string
		alter func(*protocol.EncryptedTransportBootstrap)
	}{
		{name: "wrong token", token: "wrong-agent-secret", alter: func(*protocol.EncryptedTransportBootstrap) {}},
		{name: "tampered ciphertext", token: "agent-secret", alter: func(envelope *protocol.EncryptedTransportBootstrap) { envelope.Ciphertext += "A" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := sealedTransportConfig(t, "agent-secret", "worker-gpu0", 7, testTransportBootstrap("frps.example.test"))
			test.alter(config.Transport)
			gateway := newTransportGatewayFake(config)
			waiter := newManualTransportWaiter()
			var mu sync.Mutex
			var logs []string
			manager := &TransportManager{AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, AgentToken: test.token,
				SwapPort: 6006, Gateway: gateway, Factory: newTransportFactoryFake(), RetryMin: time.Second,
				RetryMax: time.Second, Wait: waiter.Wait, Logf: func(format string, args ...any) {
					mu.Lock()
					logs = append(logs, fmt.Sprintf(format, args...))
					mu.Unlock()
				}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- manager.Run(ctx) }()
			receiveWithTimeout(t, gateway.configCalls)
			receiveWithTimeout(t, waiter.calls)
			if snapshot := manager.Snapshot(); snapshot.Ready || snapshot.LlamaSwapURL != "" {
				t.Fatalf("invalid bootstrap published state: %+v", snapshot)
			}
			if gateway.acquireCount() != 0 {
				t.Fatalf("lease acquired for invalid bootstrap")
			}
			cancel()
			if err := receiveWithTimeout(t, done); err != nil {
				t.Fatalf("Run returned %v", err)
			}
			mu.Lock()
			joined := strings.Join(logs, "\n")
			mu.Unlock()
			for _, secret := range []string{"agent-secret", "wrong-agent-secret", "frp-secret", "llama-secret"} {
				if strings.Contains(joined, secret) {
					t.Fatalf("logs leak %q: %q", secret, joined)
				}
			}
		})
	}
}

func TestTransportManagerRejectsMalformedBootstrapBeforeLeaseOrClient(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*transportcrypto.Bootstrap)
	}{
		{name: "URL server", mutate: func(value *transportcrypto.Bootstrap) { value.ServerAddr = "https://frps.example.test" }},
		{name: "host port", mutate: func(value *transportcrypto.Bootstrap) { value.ServerAddr = "frps.example.test:7000" }},
		{name: "bracketed IPv6", mutate: func(value *transportcrypto.Bootstrap) { value.ServerAddr = "[2001:db8::8]" }},
		{name: "path", mutate: func(value *transportcrypto.Bootstrap) { value.ServerAddr = "frps.example.test/path" }},
		{name: "internal whitespace", mutate: func(value *transportcrypto.Bootstrap) { value.ServerAddr = "frps .example.test" }},
		{name: "invalid hostname", mutate: func(value *transportcrypto.Bootstrap) { value.ServerAddr = "-frps.example.test" }},
		{name: "empty FRP token", mutate: func(value *transportcrypto.Bootstrap) { value.AuthToken = "" }},
		{name: "invalid remote range", mutate: func(value *transportcrypto.Bootstrap) { value.PortStart, value.PortEnd = 3000, 2000 }},
		{name: "empty llama token", mutate: func(value *transportcrypto.Bootstrap) { value.LlamaSwapToken = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bootstrap := testTransportBootstrap("frps.example.test")
			test.mutate(&bootstrap)
			gateway := newTransportGatewayFake(sealedTransportConfig(t, "agent-secret", "worker-gpu0", 7, bootstrap))
			gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "must-not-use", Slot: 2, RemotePort: 2002, Generation: 7})
			client := newFRPClientFake()
			client.ready <- errors.New("must not start")
			factory := newTransportFactoryFake(client)
			waiter := newManualTransportWaiter()
			var mu sync.Mutex
			var logs []string
			manager := &TransportManager{AgentID: "worker-gpu0", AgentToken: "agent-secret", SwapPort: 6006,
				Gateway: gateway, Factory: factory, RetryMin: time.Second, RetryMax: time.Second, Wait: waiter.Wait,
				Logf: func(format string, args ...any) {
					mu.Lock()
					logs = append(logs, fmt.Sprintf(format, args...))
					mu.Unlock()
				}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- manager.Run(ctx) }()
			receiveWithTimeout(t, waiter.calls)
			cancel()
			if err := receiveWithTimeout(t, done); err != nil {
				t.Fatalf("Run returned %v", err)
			}
			if gateway.acquireCount() != 0 || factory.createCount() != 0 || manager.Snapshot().Ready {
				t.Fatalf("invalid bootstrap acquired=%d clients=%d state=%+v", gateway.acquireCount(), factory.createCount(), manager.Snapshot())
			}
			assertNoTransportSecretOrValue(t, logs, bootstrap.ServerAddr, bootstrap.AuthToken, bootstrap.LlamaSwapToken, "must-not-use")
		})
	}
}

func TestTransportManagerRejectsMalformedLeaseWithoutClientPublishOrRelease(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name  string
		lease protocol.TransportLeaseResponse
	}{
		{name: "generation mismatch", lease: protocol.TransportLeaseResponse{LeaseID: "leaky-id", Slot: 2, RemotePort: 2002, Generation: 8, ExpiresAt: now.Add(time.Minute)}},
		{name: "empty lease ID", lease: protocol.TransportLeaseResponse{Slot: 2, RemotePort: 2002, Generation: 7, ExpiresAt: now.Add(time.Minute)}},
		{name: "negative slot", lease: protocol.TransportLeaseResponse{LeaseID: "leaky-id", Slot: -1, RemotePort: 2000, Generation: 7, ExpiresAt: now.Add(time.Minute)}},
		{name: "overflow slot", lease: protocol.TransportLeaseResponse{LeaseID: "leaky-id", Slot: int(^uint(0) >> 1), RemotePort: 2002, Generation: 7, ExpiresAt: now.Add(time.Minute)}},
		{name: "wrong derived port", lease: protocol.TransportLeaseResponse{LeaseID: "leaky-id", Slot: 2, RemotePort: 2003, Generation: 7, ExpiresAt: now.Add(time.Minute)}},
		{name: "port outside range", lease: protocol.TransportLeaseResponse{LeaseID: "leaky-id", Slot: 1001, RemotePort: 3001, Generation: 7, ExpiresAt: now.Add(time.Minute)}},
		{name: "expired", lease: protocol.TransportLeaseResponse{LeaseID: "leaky-id", Slot: 2, RemotePort: 2002, Generation: 7, ExpiresAt: now.Add(-time.Minute)}},
		{name: "missing expiry", lease: protocol.TransportLeaseResponse{LeaseID: "leaky-id", Slot: 2, RemotePort: 2002, Generation: 7}},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := newTransportGatewayFake(sealedTransportConfig(t, "agent-secret", "worker-gpu0", 7, testTransportBootstrap("frps.example.test")))
			gateway.leases = []protocol.TransportLeaseResponse{test.lease}
			client := newFRPClientFake()
			client.ready <- errors.New("must not start")
			factory := newTransportFactoryFake(client)
			waiter := newManualTransportWaiter()
			var mu sync.Mutex
			var logs []string
			manager := &TransportManager{AgentID: "worker-gpu0", AgentToken: "agent-secret", SwapPort: 6006,
				Gateway: gateway, Factory: factory, RetryMin: time.Second, RetryMax: time.Second, Wait: waiter.Wait,
				Logf: func(format string, args ...any) {
					mu.Lock()
					logs = append(logs, fmt.Sprintf(format, args...))
					mu.Unlock()
				}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- manager.Run(ctx) }()
			receiveWithTimeout(t, waiter.calls)
			cancel()
			if err := receiveWithTimeout(t, done); err != nil {
				t.Fatalf("Run returned %v", err)
			}
			if factory.createCount() != 0 || manager.Snapshot().Ready {
				t.Fatalf("invalid lease clients=%d state=%+v", factory.createCount(), manager.Snapshot())
			}
			select {
			case release := <-gateway.releaseCalls:
				t.Fatalf("invalid lease released with untrusted fields: %+v", release)
			default:
			}
			assertNoTransportSecretOrValue(t, logs, test.lease.LeaseID, strconv.Itoa(test.lease.RemotePort), "frp-secret", "llama-secret")
		})
	}
}

func TestTransportManagerMalformedReplacementKeepsKnownLeaseAndReleasesItOnShutdown(t *testing.T) {
	const agentID, agentToken = "worker-gpu0", "agent-secret"
	gateway := newTransportGatewayFake(sealedTransportConfig(t, agentToken, agentID, 7, testTransportBootstrap("frps.example.test")))
	gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "known-suspect", Slot: 2, RemotePort: 2002, Generation: 7})
	gateway.leases = append(gateway.leases, protocol.TransportLeaseResponse{
		LeaseID: "untrusted-response", Slot: 99, RemotePort: 2999, Generation: 999, ExpiresAt: time.Now().Add(time.Minute),
	})
	client := newFRPClientFake()
	factory := newTransportFactoryFake(client)
	waiter := newManualTransportWaiter()
	manager := &TransportManager{AgentID: agentID, AgentToken: agentToken, SwapPort: 6006,
		Gateway: gateway, Factory: factory, RetryMin: time.Second, RetryMax: time.Second, Wait: waiter.Wait, ReleaseTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	receiveWithTimeout(t, gateway.acquireCalls)
	receiveWithTimeout(t, factory.created)
	client.ready <- errors.New("not ready")
	receiveWithTimeout(t, client.closed)
	waiter.advance(t)
	receiveWithTimeout(t, gateway.configCalls)
	replacement := receiveWithTimeout(t, gateway.acquireCalls)
	if replacement.LeaseID != "known-suspect" || replacement.Generation != 7 || len(replacement.ExcludeSlots) != 1 || replacement.ExcludeSlots[0] != 2 {
		t.Fatalf("replacement request = %+v", replacement)
	}
	receiveWithTimeout(t, waiter.calls)
	cancel()
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	release := receiveWithTimeout(t, gateway.releaseCalls)
	if release.LeaseID != "known-suspect" || release.Generation != 7 {
		t.Fatalf("shutdown release = %+v", release)
	}
	if release.LeaseID == "untrusted-response" || release.Generation == 999 {
		t.Fatalf("released untrusted response fields: %+v", release)
	}
}

func assertNoTransportSecretOrValue(t *testing.T, logs []string, values ...string) {
	t.Helper()
	joined := strings.Join(logs, "\n")
	for _, value := range values {
		if value != "" && strings.Contains(joined, value) {
			t.Fatalf("transport logs leak %q: %q", value, joined)
		}
	}
}

func TestTransportManagerLeaseRetryBackoffIsBounded(t *testing.T) {
	const agentID, agentToken = "worker-gpu0", "agent-secret"
	gateway := newTransportGatewayFake(sealedTransportConfig(t, agentToken, agentID, 7, testTransportBootstrap("frps.example.test")))
	gateway.leaseErrs = []error{errors.New("capacity"), errors.New("capacity"), errors.New("capacity"), errors.New("capacity")}
	waiter := newManualTransportWaiter()
	manager := &TransportManager{AgentID: agentID, AgentToken: agentToken, SwapPort: 6006,
		Gateway: gateway, Factory: newTransportFactoryFake(), RetryMin: time.Second, RetryMax: 4 * time.Second, Wait: waiter.Wait}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
		receiveWithTimeout(t, gateway.configCalls)
		receiveWithTimeout(t, gateway.acquireCalls)
		call := receiveWithTimeout(t, waiter.calls)
		if call.delay != want {
			t.Fatalf("retry delay = %s, want %s", call.delay, want)
		}
		if want == 4*time.Second && gateway.acquireCount() == 4 {
			cancel()
			break
		}
		close(call.release)
	}
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if manager.Snapshot().Ready {
		t.Fatal("capacity failure published transport")
	}
}

func TestTransportManagerSnapshotIsSafeDuringFatalReplacement(t *testing.T) {
	manager, gateway, factory, first, waiter, cancel, done := startReadyTransportManager(t, "frps.example.test", 7)
	second := newFRPClientFake()
	factory.addClient(second)
	gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "replacement", Slot: 4, RemotePort: 2004, Generation: 7})

	readersDone := make(chan struct{})
	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-readersDone:
					return
				default:
					_ = manager.Snapshot()
				}
			}
		}()
	}
	first.runErr <- errors.New("fatal")
	receiveWithTimeout(t, first.closed)
	waiter.advanceDuration(t, time.Second)
	receiveWithTimeout(t, gateway.configCalls)
	receiveWithTimeout(t, gateway.acquireCalls)
	receiveWithTimeout(t, factory.created)
	second.ready <- nil
	eventually(t, func() bool { return manager.Snapshot().Ready })
	cancel()
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	close(readersDone)
	readers.Wait()
}

func startReadyTransportManager(t *testing.T, serverAddr string, generation uint64) (*TransportManager, *transportGatewayFake, *transportFactoryFake, *frpClientFake, *manualTransportWaiter, context.CancelFunc, <-chan error) {
	t.Helper()
	const agentToken = "agent-secret"
	const agentID = "worker-gpu0"
	gateway := newTransportGatewayFake(sealedTransportConfig(t, agentToken, agentID, generation, testTransportBootstrap(serverAddr)))
	gateway.addLease(protocol.TransportLeaseResponse{LeaseID: "lease-" + strconv.FormatUint(generation, 10), Slot: 3, RemotePort: 2003, Generation: generation})
	client := newFRPClientFake()
	factory := newTransportFactoryFake(client)
	waiter := newManualTransportWaiter()
	manager := &TransportManager{
		AgentID: agentID, Tags: []string{"gpu-4090"}, AgentToken: agentToken, SwapPort: 6006,
		Gateway: gateway, Factory: factory, PollInterval: time.Minute,
		RetryMin: time.Second, RetryMax: time.Second, ReleaseTimeout: time.Second, Wait: waiter.Wait,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	receiveWithTimeout(t, gateway.configCalls)
	receiveWithTimeout(t, gateway.acquireCalls)
	receiveWithTimeout(t, factory.created)
	client.ready <- nil
	eventually(t, func() bool { return manager.Snapshot().Ready })
	return manager, gateway, factory, client, waiter, cancel, done
}

func stopTransportManager(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := receiveWithTimeout(t, done); err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func sealedTransportConfig(t *testing.T, token, agentID string, generation uint64, bootstrap transportcrypto.Bootstrap) protocol.AgentConfigResponse {
	t.Helper()
	envelope, err := transportcrypto.SealBootstrap(token, agentID, generation, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.AgentConfigResponse{Transport: &envelope}
}

func testTransportBootstrap(serverAddr string) transportcrypto.Bootstrap {
	return transportcrypto.Bootstrap{
		Type: "frp_tcp", ServerAddr: serverAddr, ServerPort: 7000, AuthToken: "frp-secret",
		PortStart: 2000, PortEnd: 3000, LeaseTTLSeconds: 180, LlamaSwapToken: "llama-secret",
	}
}

type transportGatewayFake struct {
	mu           sync.Mutex
	config       protocol.AgentConfigResponse
	configErr    error
	leases       []protocol.TransportLeaseResponse
	leaseErrs    []error
	acquireCalls chan protocol.TransportLeaseRequest
	releaseCalls chan protocol.TransportLeaseRequest
	configCalls  chan struct{}
	acquires     int
	releaseFn    func(context.Context, protocol.TransportLeaseRequest) error
	events       chan string
}

func (g *transportGatewayFake) GetConfigForAgentContext(_ context.Context, _ string, _ []string) (protocol.AgentConfigResponse, error) {
	if g.configCalls != nil {
		g.configCalls <- struct{}{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.config, g.configErr
}

func (g *transportGatewayFake) RequestTransportLeaseContext(_ context.Context, request protocol.TransportLeaseRequest) (protocol.TransportLeaseResponse, error) {
	if g.acquireCalls == nil {
		g.acquireCalls = make(chan protocol.TransportLeaseRequest, 32)
	}
	g.acquireCalls <- request
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acquires++
	var response protocol.TransportLeaseResponse
	if len(g.leases) > 0 {
		response, g.leases = g.leases[0], g.leases[1:]
	}
	var err error
	if len(g.leaseErrs) > 0 {
		err, g.leaseErrs = g.leaseErrs[0], g.leaseErrs[1:]
	}
	return response, err
}

func newTransportGatewayFake(config protocol.AgentConfigResponse) *transportGatewayFake {
	return &transportGatewayFake{
		config: config, acquireCalls: make(chan protocol.TransportLeaseRequest, 32),
		releaseCalls: make(chan protocol.TransportLeaseRequest, 32), configCalls: make(chan struct{}, 32),
	}
}

func (g *transportGatewayFake) setConfig(config protocol.AgentConfigResponse) {
	g.mu.Lock()
	g.config = config
	g.mu.Unlock()
}

func (g *transportGatewayFake) addLease(lease protocol.TransportLeaseResponse) {
	g.mu.Lock()
	if lease.ExpiresAt.IsZero() {
		lease.ExpiresAt = time.Now().Add(time.Minute)
	}
	g.leases = append(g.leases, lease)
	g.mu.Unlock()
}

func (g *transportGatewayFake) acquireCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.acquires
}

func (g *transportGatewayFake) ReleaseTransportLeaseContext(ctx context.Context, request protocol.TransportLeaseRequest) error {
	if g.releaseCalls == nil {
		g.releaseCalls = make(chan protocol.TransportLeaseRequest, 32)
	}
	g.releaseCalls <- request
	if g.events != nil {
		g.events <- "release"
	}
	if g.releaseFn != nil {
		return g.releaseFn(ctx, request)
	}
	return nil
}

type transportFactoryFake struct {
	mu           sync.Mutex
	clients      []*frpClientFake
	created      chan FRPProxyConfig
	createdCount int
}

func newTransportFactoryFake(clients ...*frpClientFake) *transportFactoryFake {
	return &transportFactoryFake{clients: clients, created: make(chan FRPProxyConfig, 32)}
}

func (f *transportFactoryFake) New(config FRPProxyConfig) (FRPClient, error) {
	if f.created == nil {
		f.created = make(chan FRPProxyConfig, 32)
	}
	f.created <- config
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdCount++
	if len(f.clients) == 0 {
		return nil, errors.New("no fake FRP client")
	}
	client := f.clients[0]
	f.clients = f.clients[1:]
	return client, nil
}

func (f *transportFactoryFake) addClient(client *frpClientFake) {
	f.mu.Lock()
	f.clients = append(f.clients, client)
	f.mu.Unlock()
}

func (f *transportFactoryFake) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createdCount
}

type frpClientFake struct {
	ready        chan error
	runErr       chan error
	closed       chan struct{}
	once         sync.Once
	events       chan string
	closeHook    func()
	closeInspect func()
	closeBlock   chan struct{}
	runExitBlock chan struct{}
	activeCalls  atomic.Int32
}

type transportWaitCall struct {
	release chan struct{}
	delay   time.Duration
}

type manualTransportWaiter struct {
	calls chan transportWaitCall
}

func newManualTransportWaiter() *manualTransportWaiter {
	return &manualTransportWaiter{calls: make(chan transportWaitCall, 32)}
}

func (w *manualTransportWaiter) Wait(ctx context.Context, delay time.Duration) error {
	call := transportWaitCall{release: make(chan struct{}), delay: delay}
	w.calls <- call
	select {
	case <-call.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *manualTransportWaiter) advance(t *testing.T) {
	t.Helper()
	call := receiveWithTimeout(t, w.calls)
	close(call.release)
}

func (w *manualTransportWaiter) advanceDuration(t *testing.T, want time.Duration) {
	t.Helper()
	for {
		call := receiveWithTimeout(t, w.calls)
		if call.delay == want {
			close(call.release)
			return
		}
	}
}

func newFRPClientFake() *frpClientFake {
	return &frpClientFake{ready: make(chan error, 1), runErr: make(chan error, 1), closed: make(chan struct{})}
}

func (c *frpClientFake) Run(ctx context.Context) error {
	c.activeCalls.Add(1)
	defer c.activeCalls.Add(-1)
	select {
	case err := <-c.runErr:
		return err
	case <-ctx.Done():
		if c.runExitBlock != nil {
			<-c.runExitBlock
		}
		return ctx.Err()
	}
}

func (c *frpClientFake) WaitReady(ctx context.Context) error {
	c.activeCalls.Add(1)
	defer c.activeCalls.Add(-1)
	select {
	case err := <-c.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *frpClientFake) Close() error {
	c.once.Do(func() {
		if c.closeInspect != nil {
			c.closeInspect()
		}
		if c.closeBlock != nil {
			<-c.closeBlock
		}
		if c.events != nil {
			c.events <- "close"
		}
		close(c.closed)
		if c.closeHook != nil {
			c.closeHook()
		}
	})
	return nil
}

func receiveWithTimeout[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for test event")
		var zero T
		return zero
	}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied")
		}
		runtime.Gosched()
	}
}
